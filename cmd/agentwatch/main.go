package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/agentwatch/agentwatch/internal/ipc"
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
		log.Printf("Failed to send event: %v", err)
		return
	}
	defer resp.Body.Close()
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

	// Send Initial Event
	sendEvent(client, ipc.Event{
		SessionID: sessionID,
		AgentName: agentName,
		Status:    ipc.StatusRunning,
	})

	cmd := exec.Command(agentName, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Handle graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		sendEvent(client, ipc.Event{
			SessionID: sessionID,
			AgentName: agentName,
			Status:    ipc.StatusFinished,
			Message:   "Interrupted",
		})
		os.Exit(0)
	}()

	err := cmd.Run()

	status := ipc.StatusFinished
	message := "Completed"
	if err != nil {
		status = ipc.StatusError
		message = err.Error()
	}

	sendEvent(client, ipc.Event{
		SessionID: sessionID,
		AgentName: agentName,
		Status:    status,
		Message:   message,
	})

	if err != nil {
		os.Exit(1)
	}
}
