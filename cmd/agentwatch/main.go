package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/agentwatch/agentwatch/internal/ipc"
	"github.com/creack/pty"
	"golang.org/x/term"
)

func sendEvent(client *http.Client, event ipc.Event) {
	event.PID = os.Getpid()
	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("Failed to marshal event: %v", err)
		return
	}

	req, err := http.NewRequest("POST", "http://"+ipc.ServerAddress+"/event", bytes.NewReader(data))
	if err != nil {
		log.Printf("Failed to create request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		// silently ignore connection errors so we don't spam the terminal
		return
	}
	defer resp.Body.Close()
}

type ParserWriter struct {
	Target    io.Writer
	Client    *http.Client
	SessionID string
	AgentName string

	mu             sync.Mutex
	lastStatus     ipc.AgentStatus
	outputBuffer   []byte
	timer          *time.Timer
	lastInputTime  time.Time
	codexTitleSeen bool
	codexTitleBusy bool
}

var ansiRegex = regexp.MustCompile(`\x1b\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[\x20-\x2f]?[\x30-\x7e]`)
var codexTitleRegex = regexp.MustCompile(`\x1b\]0;([^\x07\x1b]*)(?:\x07|\x1b\\)`)
var brailleSpinnerRegex = regexp.MustCompile(`[⠁-⣿]`)

func (pw *ParserWriter) Write(p []byte) (n int, err error) {
	// Always write to target (os.Stdout)
	n, err = pw.Target.Write(p)

	pw.mu.Lock()
	defer pw.mu.Unlock()

	// Append to buffer
	pw.outputBuffer = append(pw.outputBuffer, p...)
	// Keep buffer small (last 4096 bytes is enough for prompt detection and autocomplete noise)
	if len(pw.outputBuffer) > 4096 {
		pw.outputBuffer = pw.outputBuffer[len(pw.outputBuffer)-4096:]
	}
	pw.updateCodexTitleLocked()

	// Synchronously evaluate state on write instead of blindly setting Running.
	// This prevents the echoed user typing at the prompt from resetting the state to Running.
	if pw.isCurrentlyIdleLocked() {
		pw.setStatus(ipc.StatusWaiting)
	} else {
		pw.setStatus(ipc.StatusRunning)
	}

	// Debounce timer for idle detection
	if pw.timer != nil {
		pw.timer.Stop()
	}
	pw.timer = time.AfterFunc(100*time.Millisecond, pw.checkIdle)

	return n, err
}

// Expects pw.mu to be held
func (pw *ParserWriter) isCurrentlyIdleLocked() bool {
	outStr := string(pw.outputBuffer)

	// Strip ANSI escape codes (handles CSI, OSC, and standard 2-character escapes)
	cleanStr := ansiRegex.ReplaceAllString(outStr, "")
	cleanStr = strings.TrimSpace(cleanStr)

	// Check if the user is currently typing and the prompt is present anywhere in the buffer.
	// We allow a 2-second grace period after user typing.
	isTypingGracePeriod := !pw.lastInputTime.IsZero() && time.Since(pw.lastInputTime) < 2*time.Second

	// Replace carriage returns with newlines to normalize TUI line overwrites
	normalizedStr := strings.ReplaceAll(cleanStr, "\r", "\n")
	lines := strings.Split(normalizedStr, "\n")

	// Get the last 5 non-empty lines of visual output
	var lastLines []string
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed != "" {
			lastLines = append([]string{trimmed}, lastLines...)
			if len(lastLines) >= 5 {
				break
			}
		}
	}

	if pw.isCodex() {
		if pw.codexTitleSeen {
			return !pw.codexTitleBusy
		}
		return codexIsIdle(lastLines, normalizedStr, isTypingGracePeriod)
	}

	// First, check if the very last line of visual output displays an idle status bar indicator.
	// If it does, the agent is definitely waiting/idle, overriding any older busy indicators in history.
	if len(lastLines) > 0 {
		lastLine := strings.ToLower(lastLines[len(lastLines)-1])
		if strings.Contains(lastLine, "? for shortcuts") ||
			strings.Contains(lastLine, "← for agents") ||
			strings.Contains(lastLine, "ctrl-c again to exit") {
			return true
		}
	}

	// 1. Check if the recent output contains active busy indicators.
	// If any of these are present in the last 5 lines, the agent is definitely busy.
	for _, line := range lastLines {
		lowerLine := strings.ToLower(line)
		if strings.Contains(lowerLine, "esc to interrupt") ||
			strings.Contains(lowerLine, "esc to cancel") ||
			strings.Contains(lowerLine, "generating...") ||
			strings.Contains(lowerLine, "booping") ||
			strings.Contains(lowerLine, "thinking...") ||
			strings.Contains(lowerLine, "working...") {
			return false
		}

		// Precise check for active spinners.
		// Spinners can be single characters on a line or start a line followed by an ellipsis (e.g. "✢Noodling…")
		spinners := map[string]bool{
			"⣾": true, "⣽": true, "⣻": true, "⢿": true, "⡿": true, "⣟": true, "⣯": true, "⣷": true,
			"✢": true, "✳": true, "✶": true, "✻": true, "·": true,
		}
		if spinners[line] {
			return false
		}
		for s := range spinners {
			if strings.HasPrefix(line, s) && (strings.HasSuffix(line, "…") || strings.HasSuffix(line, "...")) {
				return false
			}
		}
	}

	isPromptPresent := false
	if isTypingGracePeriod {
		// If user is typing, we check if the prompt is present ANYWHERE in the normalized buffer
		containsPatterns := []string{"❯", "User:", ">", "$"}
		for _, pattern := range containsPatterns {
			if strings.Contains(normalizedStr, pattern) {
				isPromptPresent = true
				break
			}
		}
	} else {
		// If not in the grace period, check if the prompt is in the last 5 visual lines.
		for _, line := range lastLines {
			// Check specific contains indicators anywhere on the line
			if strings.Contains(line, "❯") || strings.Contains(line, "User:") {
				isPromptPresent = true
				break
			}
			// Check prefix indicators at the start of the line (e.g. > or $)
			if strings.HasPrefix(line, ">") || strings.HasPrefix(line, "$") {
				isPromptPresent = true
				break
			}
		}
	}

	if isPromptPresent {
		return true
	}

	// 2. If the entire trimmed output ends with standard prompt/question suffixes (like >, ?, $)
	// We exclude : and ) here to prevent false positives from active output (like tool calls or timestamps)
	suffixPatterns := []string{
		">",
		"?",
		"$",
	}
	for _, pattern := range suffixPatterns {
		if strings.HasSuffix(cleanStr, pattern) {
			return true
		}
	}

	return false
}

// Codex reports its live state in the terminal title: a Braille spinner while
// working and the plain workspace title when it is ready for another prompt.
// This is more reliable than trying to reconstruct Codex's cursor-based TUI.
func (pw *ParserWriter) updateCodexTitleLocked() {
	if !pw.isCodex() {
		return
	}
	matches := codexTitleRegex.FindAllSubmatch(pw.outputBuffer, -1)
	if len(matches) == 0 {
		return
	}
	title := string(matches[len(matches)-1][1])
	pw.codexTitleSeen = true
	pw.codexTitleBusy = brailleSpinnerRegex.MatchString(title)
}

func (pw *ParserWriter) isCodex() bool {
	name := strings.ToLower(strings.TrimSpace(pw.AgentName))
	return name == "codex" || strings.HasSuffix(name, "/codex")
}

// codexIsIdle follows the same ordering as the Claude/Antigravity parser:
// Codex's ready footer is authoritative, then current activity wins over an
// older composer line retained in the PTY buffer.
func codexIsIdle(lastLines []string, output string, typingGrace bool) bool {
	if len(lastLines) > 0 {
		lastLine := strings.ToLower(lastLines[len(lastLines)-1])
		if strings.Contains(lastLine, "? for shortcuts") ||
			strings.Contains(lastLine, "← for agents") ||
			strings.Contains(lastLine, "ctrl-c again to exit") ||
			strings.HasPrefix(strings.TrimSpace(lastLines[len(lastLines)-1]), "›") {
			return true
		}
	}

	for _, line := range lastLines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "esc to interrupt") ||
			strings.Contains(lower, "esc to cancel") ||
			strings.Contains(lower, "working") ||
			strings.Contains(lower, "thinking") ||
			strings.Contains(lower, "planning") ||
			strings.Contains(lower, "executing") ||
			strings.Contains(lower, "exploring") ||
			strings.Contains(lower, "searching") ||
			strings.Contains(lower, "reading") {
			return false
		}
	}

	for _, line := range lastLines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "do you want to proceed") ||
			strings.Contains(lower, "allow once") ||
			strings.Contains(lower, "allow for this session") ||
			strings.Contains(lower, "approve this command") ||
			strings.Contains(lower, "approval required") ||
			strings.Contains(lower, "requires approval") ||
			strings.Contains(lower, "press enter to continue") ||
			strings.HasPrefix(strings.TrimSpace(line), "›") {
			return true
		}
	}

	return typingGrace && strings.Contains(output, "›")
}

func (pw *ParserWriter) checkIdle() {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	if pw.isCurrentlyIdleLocked() {
		pw.setStatus(ipc.StatusWaiting)
	} else {
		pw.setStatus(ipc.StatusRunning)
	}
}

func (pw *ParserWriter) setStatus(status ipc.AgentStatus) {
	if pw.lastStatus != status {
		pw.lastStatus = status
		go sendEvent(pw.Client, ipc.Event{
			SessionID: pw.SessionID,
			AgentName: pw.AgentName,
			Status:    status,
			Message:   string(status),
		})
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: agentwatch <command> [args...]")
		os.Exit(1)
	}

	agentName := os.Args[1]
	args := os.Args[2:]
	if isCodexInteractive(agentName, args) && !hasArgument(args, "--no-alt-screen") {
		// Codex's alternate screen only emits cursor updates, so the wrapper cannot
		// reliably see its final ready prompt. Inline mode preserves that state.
		args = append(args, "--no-alt-screen")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	sessionID := fmt.Sprintf("%s-%d", agentName, time.Now().Unix())

	cmd := exec.Command(agentName, args...)

	// Start with PTY
	ptmx, err := pty.Start(cmd)
	if err != nil {
		// Fallback to normal execution if PTY fails
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		sendEvent(client, ipc.Event{SessionID: sessionID, AgentName: agentName, Status: ipc.StatusRunning})
		cmd.Run()
		sendEvent(client, ipc.Event{SessionID: sessionID, AgentName: agentName, Status: ipc.StatusFinished})
		return
	}
	defer ptmx.Close()

	// Handle window resize
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			_ = pty.InheritSize(os.Stdin, ptmx)
		}
	}()
	ch <- syscall.SIGWINCH

	// Put stdin in raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err == nil {
		defer term.Restore(int(os.Stdin.Fd()), oldState)
	}

	// Handle graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	go func() {
		<-sig
		sendEvent(client, ipc.Event{
			SessionID: sessionID,
			AgentName: agentName,
			Status:    ipc.StatusFinished,
			Message:   "Interrupted",
		})
		if oldState != nil {
			term.Restore(int(os.Stdin.Fd()), oldState)
		}
		os.Exit(0)
	}()

	pw := &ParserWriter{
		Target:    os.Stdout,
		Client:    client,
		SessionID: sessionID,
		AgentName: agentName,
	}

	// Set initial initializing state
	pw.setStatus(ipc.StatusInitializing)

	// Copy stdin to pty, and pty to parser
	// Copy stdin to pty with input tracking. If user types non-Enter keys,
	// we record the timestamp to enable a 2-second grace period where we look
	// for the prompt anywhere in the output buffer. If they hit Enter, we cancel
	// the grace period immediately to allow transition back to Running.
	go func() {
		buf := make([]byte, 128)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				break
			}
			if n > 0 {
				pw.mu.Lock()
				hasEnter := false
				for i := 0; i < n; i++ {
					if buf[i] == '\n' || buf[i] == '\r' {
						hasEnter = true
						break
					}
				}
				if hasEnter {
					pw.lastInputTime = time.Time{}
				} else {
					pw.lastInputTime = time.Now()
				}
				pw.mu.Unlock()

				_, err = ptmx.Write(buf[:n])
				if err != nil {
					break
				}
			}
		}
	}()
	io.Copy(pw, ptmx)

	// Clean up when command finishes
	sendEvent(client, ipc.Event{
		SessionID: sessionID,
		AgentName: agentName,
		Status:    ipc.StatusFinished,
		Message:   "Completed",
	})
}

func isCodexInteractive(agentName string, args []string) bool {
	name := strings.ToLower(strings.TrimSpace(agentName))
	if name != "codex" && !strings.HasSuffix(name, "/codex") {
		return false
	}

	// The root command is the interactive TUI. These commands either run to
	// completion or manage local Codex state, so they should retain their args.
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		switch arg {
		case "exec", "e", "review", "login", "logout", "mcp", "plugin", "mcp-server", "app-server", "remote-control", "app", "completion", "update", "doctor", "sandbox", "debug", "apply", "a", "archive", "delete", "unarchive", "fork", "cloud", "exec-server", "features", "help":
			return false
		}
		break
	}
	return true
}

func hasArgument(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}
