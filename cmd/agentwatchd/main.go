package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/agentwatch/agentwatch/internal/ipc"
)

type Daemon struct {
	mu       sync.Mutex
	sessions map[string]ipc.Event
}

func NewDaemon() *Daemon {
	return &Daemon{
		sessions: make(map[string]ipc.Event),
	}
}

func (d *Daemon) handleEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var event ipc.Event
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	d.mu.Lock()
	d.sessions[event.SessionID] = event
	d.mu.Unlock()

	log.Printf("Received event: [%s] %s - %s: %s\n", event.SessionID, event.AgentName, event.Status, event.Message)

	w.WriteHeader(http.StatusOK)
}

func (d *Daemon) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d.sessions)
}

func main() {
	daemon := NewDaemon()
	mux := http.NewServeMux()
	mux.HandleFunc("/event", daemon.handleEvent)
	mux.HandleFunc("/status", daemon.handleStatus)

	server := http.Server{
		Handler: mux,
	}

	listener, err := net.Listen("tcp", ipc.ServerAddress)
	if err != nil {
		log.Fatalf("Failed to listen on TCP %s: %v", ipc.ServerAddress, err)
	}

	fmt.Printf("AgentWatch Daemon listening on http://%s\n", ipc.ServerAddress)
	if err := server.Serve(listener); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
