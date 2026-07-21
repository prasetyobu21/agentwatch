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
	"github.com/agentwatch/agentwatch/internal/terminal"
	"github.com/creack/pty"
	"golang.org/x/term"
)

func sendEvent(client *http.Client, event ipc.AgentEvent) {
	event.PID = os.Getpid()
	if event.Version == 0 {
		event.Version = 1
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("Failed to marshal event: %v", err)
		return
	}

	req, err := http.NewRequest("POST", "http://"+ipc.ServerAddress+"/v1/event", bytes.NewReader(data))
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
	deliver   func(ipc.AgentEvent)

	mu                sync.Mutex
	lastState         ipc.AgentState
	sequence          uint64
	outputBuffer      []byte
	timer             *time.Timer
	lastInputTime     time.Time
	permissionShownAt time.Time
	codexTitleSeen    bool
	codexTitleBusy    bool
	codexTitle        string
	codexTitleChanged bool
	terminal          *terminal.Model
	recentInput       []inputRecord
}

type inputRecord struct {
	kind string
	at   time.Time
}

var ansiRegex = regexp.MustCompile(`\x1b\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[\x20-\x2f]?[\x30-\x7e]`)
var codexTitleRegex = regexp.MustCompile(`\x1b\]0;([^\x07\x1b]*)(?:\x07|\x1b\\)`)
var brailleSpinnerRegex = regexp.MustCompile(`[⠁-⣿]`)
var permissionMarkers = []string{
	"allow once",
	"allow for this session",
	"always allow",
	"allow this command",
	"approve this command",
	"approval required",
	"requires approval",
	"permission required",
	"do you want to proceed",
	"would you like to proceed",
	"do you want to allow",
	"would you like to allow",
	"would you like to run the following command",
	"yes, proceed",
	"yes, and don't ask again",
	"deny",
}

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
	if pw.terminal != nil {
		_ = pw.terminal.Write(p)
	}

	pw.setStateLocked(pw.classifyLocked(), "screen-classifier")

	// Debounce timer for idle detection
	if pw.timer != nil {
		pw.timer.Stop()
	}
	pw.timer = time.AfterFunc(100*time.Millisecond, pw.checkIdle)

	return n, err
}

func (pw *ParserWriter) classifyLocked() ipc.AgentState {
	var screen string
	if pw.terminal != nil {
		screen = strings.ToLower(strings.Join(pw.terminal.Snapshot().Lines, "\n"))
	}
	if screen == "" {
		screen = strings.ToLower(ansiRegex.ReplaceAllString(string(pw.outputBuffer), ""))
	}
	recentScreen := strings.ToLower(strings.Join(pw.recentVisibleLinesLocked(), "\n"))
	// A permission request is more urgent than an ordinary prompt and must win.
	if hasPermissionRequest(screen) {
		// An Enter that submitted the user's original prompt is not an
		// approval. Only interpret Enter as approval after the permission
		// state has already been published and shown to the user.
		if pw.lastState == ipc.StatePermissionRequired && pw.lastInputKindLocked() == "enter" {
			return ipc.StatePermissionResolving
		}
		return ipc.StatePermissionRequired
	}
	// Question marks and question-like prose are common in normal model output.
	// Only raise an input alert when the visible TUI includes interaction chrome
	// that tells the user how to answer an AskUserQuestion-style prompt.
	if hasInputRequestUI(recentScreen) {
		return ipc.StateInputRequired
	}
	if pw.isCurrentlyIdleLocked() {
		return ipc.StateIdle
	}
	for _, marker := range []string{"executing ", "running command", "tool use", "editing ", "apply_patch", " rg ", " git "} {
		if strings.Contains(recentScreen, marker) {
			return ipc.StateExecutingTool
		}
	}
	for _, marker := range []string{"esc to interrupt", "esc to cancel", "generating", "thinking", "working", "planning", "searching", "reading", "⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"} {
		if strings.Contains(recentScreen, marker) {
			return ipc.StateRunning
		}
	}
	// A Codex title is durable state, so an old spinner title can survive a
	// later redraw that contains no activity. Only a newly changed busy title
	// is evidence that an idle session started working again.
	if pw.isCodex() && pw.codexTitleChanged && pw.codexTitleBusy {
		return ipc.StateRunning
	}
	// Focus changes and other terminal events can repaint an idle TUI without
	// its composer/footer. Do not turn that ambiguous redraw into activity. An
	// Enter observed at the prompt is sufficient evidence of a submitted turn.
	if pw.lastState == ipc.StateIdle {
		if pw.lastInputKindLocked() == "enter" {
			return ipc.StateRunning
		}
		return ipc.StateIdle
	}
	return ipc.StateRunning
}

// recentVisibleLinesLocked returns only what is currently painted nearest the
// cursor. Terminal output above that area often contains stale activity text,
// which must not keep a completed session in the running state.
func (pw *ParserWriter) recentVisibleLinesLocked() []string {
	var lines []string
	if pw.terminal != nil {
		lines = pw.terminal.Snapshot().Lines
	} else {
		clean := ansiRegex.ReplaceAllString(string(pw.outputBuffer), "")
		lines = strings.Split(strings.ReplaceAll(clean, "\r", "\n"), "\n")
	}
	var recent []string
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		recent = append([]string{line}, recent...)
		if len(recent) == 5 {
			break
		}
	}
	return recent
}

func hasInputRequestUI(screen string) bool {
	for _, marker := range []string{
		"enter to select",
		"enter to submit",
		"enter to confirm",
		"space to select",
		"arrow keys to navigate",
		"up/down to navigate",
		"tab to navigate",
	} {
		if strings.Contains(screen, marker) {
			return true
		}
	}
	return false
}

func hasPermissionRequest(screen string) bool {
	for _, marker := range permissionMarkers {
		if strings.Contains(screen, marker) {
			return true
		}
	}
	return false
}

func (pw *ParserWriter) lastInputKindLocked() string {
	cutoff := time.Now().Add(-2 * time.Second)
	for len(pw.recentInput) > 0 && pw.recentInput[0].at.Before(cutoff) {
		pw.recentInput = pw.recentInput[1:]
	}
	if len(pw.recentInput) == 0 {
		return ""
	}
	return pw.recentInput[len(pw.recentInput)-1].kind
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

	// Replace carriage returns with newlines to normalize TUI line overwrites.
	normalizedStr := strings.ReplaceAll(cleanStr, "\r", "\n")
	lastLines := pw.recentVisibleLinesLocked()

	if pw.isCodex() {
		if pw.codexTitleSeen {
			// The title is Codex's live state. The screen can retain an old
			// composer/footer while a tool milestone is still running.
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
		trimmedLastLine := strings.TrimSpace(lastLines[len(lastLines)-1])
		if strings.HasPrefix(trimmedLastLine, "❯") ||
			strings.HasPrefix(trimmedLastLine, "User:") ||
			strings.HasPrefix(trimmedLastLine, ">") ||
			strings.HasPrefix(trimmedLastLine, "$") {
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

	// 2. If we have no screen model, fall back to the raw output suffixes.
	// We exclude : and ) here to prevent false positives from active output (like tool calls or timestamps)
	suffixPatterns := []string{
		">",
		"?",
		"$",
	}
	if pw.terminal == nil {
		for _, pattern := range suffixPatterns {
			if strings.HasSuffix(cleanStr, pattern) {
				return true
			}
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
	pw.codexTitleChanged = false
	matches := codexTitleRegex.FindAllSubmatch(pw.outputBuffer, -1)
	if len(matches) == 0 {
		return
	}
	title := string(matches[len(matches)-1][1])
	pw.codexTitleChanged = !pw.codexTitleSeen || title != pw.codexTitle
	pw.codexTitleSeen = true
	pw.codexTitle = title
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
		if hasPermissionRequest(lower) ||
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

	pw.setStateLocked(pw.classifyLocked(), "screen-classifier")
}

func (pw *ParserWriter) setStateLocked(state ipc.AgentState, source string) {
	// Terminal TUIs frequently erase/redraw their approval menu immediately
	// after painting it. Preserve a visible request through that brief redraw,
	// but then accept the next non-permission state. This also handles approval
	// methods (such as mouse clicks) that do not produce an Enter key event.
	if pw.lastState == ipc.StatePermissionRequired && state != ipc.StatePermissionRequired {
		if pw.lastInputKindLocked() == "enter" {
			state = ipc.StatePermissionResolving
			source = "permission-input"
		} else if time.Since(pw.permissionShownAt) < time.Second {
			return
		}
	}
	if pw.lastState == ipc.StatePermissionResolving &&
		state != ipc.StateRunning && state != ipc.StateExecutingTool && state != ipc.StatePermissionRequired {
		return
	}
	if pw.lastState != state {
		pw.lastState = state
		if state == ipc.StatePermissionRequired {
			pw.permissionShownAt = time.Now()
		} else {
			pw.permissionShownAt = time.Time{}
		}
		pw.sequence++
		event := ipc.AgentEvent{
			SessionID:  pw.SessionID,
			Agent:      pw.AgentName,
			State:      state,
			Sequence:   pw.sequence,
			Confidence: 0.75,
			Summary:    string(state),
			Source:     source,
		}
		go pw.deliverEvent(event)
	}
}

func (pw *ParserWriter) setState(state ipc.AgentState, source string) {
	pw.setStateWithSummary(state, string(state), source)
}

func (pw *ParserWriter) setStateWithSummary(state ipc.AgentState, summary, source string) {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	if pw.lastState != state {
		pw.lastState = state
		pw.sequence++
		go pw.deliverEvent(ipc.AgentEvent{SessionID: pw.SessionID, Agent: pw.AgentName, State: state, Sequence: pw.sequence, Confidence: 1, Summary: summary, Source: source})
	}
}

func (pw *ParserWriter) setFinalStateWithSummary(state ipc.AgentState, summary, source string) {
	pw.mu.Lock()
	if pw.lastState == state {
		pw.mu.Unlock()
		return
	}
	pw.lastState = state
	pw.sequence++
	event := ipc.AgentEvent{SessionID: pw.SessionID, Agent: pw.AgentName, State: state, Sequence: pw.sequence, Confidence: 1, Summary: summary, Source: source}
	pw.mu.Unlock()

	// The wrapper exits immediately after this transition. Wait for delivery so
	// the terminal state cannot be abandoned in an async goroutine.
	pw.deliverEvent(event)
}

func (pw *ParserWriter) deliverEvent(event ipc.AgentEvent) {
	if pw.deliver != nil {
		pw.deliver(event)
		return
	}
	sendEvent(pw.Client, event)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: aw <command> [args...]")
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
		sendEvent(client, ipc.AgentEvent{SessionID: sessionID, Agent: agentName, State: ipc.StateRunning, Confidence: 1, Source: "process-lifecycle"})
		err := cmd.Run()
		state, summary := ipc.StateCompleted, "Completed"
		if err != nil {
			state, summary = ipc.StateFailed, err.Error()
		}
		sendEvent(client, ipc.AgentEvent{SessionID: sessionID, Agent: agentName, State: state, Confidence: 1, Summary: summary, Source: "process-lifecycle"})
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
		sendEvent(client, ipc.AgentEvent{
			SessionID:  sessionID,
			Agent:      agentName,
			State:      ipc.StateFailed,
			Confidence: 1,
			Summary:    "Interrupted",
			Source:     "process-lifecycle",
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
		terminal:  terminal.New(80, 24),
	}

	// Set initial starting state.
	pw.setState(ipc.StateStarting, "process-lifecycle")

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
				forwardedInput, userInput := normalizeFocusInput(buf[:n])
				if len(userInput) > 0 {
					pw.mu.Lock()
					hasEnter := false
					for _, inputByte := range userInput {
						if inputByte == '\n' || inputByte == '\r' {
							hasEnter = true
							break
						}
					}
					if hasEnter {
						pw.lastInputTime = time.Time{}
					} else {
						pw.lastInputTime = time.Now()
					}
					pw.recordInputLocked(userInput)
					pw.setStateLocked(pw.classifyLocked(), "input-observation")
					pw.mu.Unlock()
				}

				_, err = ptmx.Write(forwardedInput)
				if err != nil {
					break
				}
			}
		}
	}()
	io.Copy(pw, ptmx)
	exitErr := cmd.Wait()

	// Clean up when command finishes
	state, summary := ipc.StateCompleted, "Completed"
	if exitErr != nil {
		state, summary = ipc.StateFailed, exitErr.Error()
	}
	pw.setFinalStateWithSummary(state, summary, "process-lifecycle")
}

// Coding-agent TUIs use terminal focus reporting to reduce redraws while the
// terminal is in the background. Keep the child PTY logically focused so its
// completion prompt is still emitted, while excluding those control bytes from
// user-input classification.
func normalizeFocusInput(data []byte) (forwarded, userInput []byte) {
	focusIn := []byte{'\x1b', '[', 'I'}
	focusOut := []byte{'\x1b', '[', 'O'}
	if !bytes.Contains(data, focusIn) && !bytes.Contains(data, focusOut) {
		return data, data
	}

	forwarded = make([]byte, 0, len(data))
	userInput = make([]byte, 0, len(data))
	for offset := 0; offset < len(data); {
		if bytes.HasPrefix(data[offset:], focusIn) || bytes.HasPrefix(data[offset:], focusOut) {
			forwarded = append(forwarded, focusIn...)
			offset += len(focusIn)
			continue
		}
		forwarded = append(forwarded, data[offset])
		userInput = append(userInput, data[offset])
		offset++
	}
	return forwarded, userInput
}

// recordInputLocked only keeps coarse, short-lived categories. It never stores
// the user's typed content or forwards it to the daemon.
func (pw *ParserWriter) recordInputLocked(data []byte) {
	kind := "text"
	for _, b := range data {
		switch b {
		case '\r', '\n':
			kind = "enter"
		case 3:
			kind = "interrupt"
		case 9:
			kind = "tab"
		case 27:
			kind = "escape"
		}
	}
	pw.recentInput = append(pw.recentInput, inputRecord{kind: kind, at: time.Now()})
	if len(pw.recentInput) > 16 {
		pw.recentInput = pw.recentInput[len(pw.recentInput)-16:]
	}
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
