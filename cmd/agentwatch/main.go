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

	mu           sync.Mutex
	lastStatus   ipc.AgentStatus
	outputBuffer []byte
	timer        *time.Timer
}

func (pw *ParserWriter) Write(p []byte) (n int, err error) {
	// Always write to target (os.Stdout)
	n, err = pw.Target.Write(p)

	pw.mu.Lock()
	defer pw.mu.Unlock()

	// Append to buffer
	pw.outputBuffer = append(pw.outputBuffer, p...)
	// Keep buffer small (last 256 bytes is enough for prompt detection)
	if len(pw.outputBuffer) > 256 {
		pw.outputBuffer = pw.outputBuffer[len(pw.outputBuffer)-256:]
	}

	// Any output means it might be running
	pw.setStatus(ipc.StatusRunning)

	// Debounce timer for idle detection
	if pw.timer != nil {
		pw.timer.Stop()
	}
	pw.timer = time.AfterFunc(300*time.Millisecond, pw.checkIdle)

	return n, err
}

func (pw *ParserWriter) checkIdle() {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	outStr := string(pw.outputBuffer)
	
	// Strip ANSI escape codes
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	cleanStr := ansiRegex.ReplaceAllString(outStr, "")
	cleanStr = strings.TrimSpace(cleanStr)
	
	// Common agent prompts that indicate it is waiting for user input
	idlePatterns := []string{
		">",
		"?",
		"$",
		"User:",
		"❯",
	}

	isIdle := false
	for _, pattern := range idlePatterns {
		if strings.HasSuffix(cleanStr, pattern) {
			isIdle = true
			break
		}
	}

	if isIdle {
		pw.setStatus(ipc.StatusWaiting)
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
	
	// Set initial idle state
	pw.setStatus(ipc.StatusWaiting)

	// Copy stdin to pty, and pty to parser
	go io.Copy(ptmx, os.Stdin)
	io.Copy(pw, ptmx)

	// Clean up when command finishes
	sendEvent(client, ipc.Event{
		SessionID: sessionID,
		AgentName: agentName,
		Status:    ipc.StatusFinished,
		Message:   "Completed",
	})
}
