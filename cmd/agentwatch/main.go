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

	mu            sync.Mutex
	lastStatus    ipc.AgentStatus
	outputBuffer  []byte
	timer         *time.Timer
	lastInputTime time.Time
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
	pw.timer = time.AfterFunc(300*time.Millisecond, pw.checkIdle)

	return n, err
}

// Expects pw.mu to be held
func (pw *ParserWriter) isCurrentlyIdleLocked() bool {
	outStr := string(pw.outputBuffer)
	
	// Strip ANSI escape codes (handles CSI, OSC, and standard 2-character escapes)
	ansiRegex := regexp.MustCompile(`\x1b\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[\x20-\x2f]?[\x30-\x7e]`)
	cleanStr := ansiRegex.ReplaceAllString(outStr, "")
	cleanStr = strings.TrimSpace(cleanStr)

	// Check if the user is currently typing and the prompt is present anywhere in the buffer.
	// We allow a 2-second grace period after user typing.
	isTypingGracePeriod := !pw.lastInputTime.IsZero() && time.Since(pw.lastInputTime) < 2*time.Second

	// 1. Check for prompt indicators (like ❯, User:, >, $)
	containsPatterns := []string{
		"❯",
		"User:",
		">",
		"$",
	}

	isPromptPresent := false
	if isTypingGracePeriod {
		// If user is typing, we check if the prompt is present ANYWHERE in the buffer
		for _, pattern := range containsPatterns {
			if strings.Contains(cleanStr, pattern) {
				isPromptPresent = true
				break
			}
		}
	} else {
		// If not in the typing grace period, the prompt must be on the last line
		lines := strings.Split(cleanStr, "\n")
		var lastLine string
		if len(lines) > 0 {
			lastLine = strings.TrimSpace(lines[len(lines)-1])
		}
		for _, pattern := range containsPatterns {
			if strings.Contains(lastLine, pattern) {
				isPromptPresent = true
				break
			}
		}
	}

	if isPromptPresent {
		return true
	}

	// 2. If the entire trimmed output ends with standard prompt/question suffixes
	// (like >, ?, $, :, ))
	suffixPatterns := []string{
		">",
		"?",
		"$",
		":",
		")",
	}
	for _, pattern := range suffixPatterns {
		if strings.HasSuffix(cleanStr, pattern) {
			return true
		}
	}

	return false
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
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
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
