package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/agentwatch/agentwatch/internal/ipc"
)

const eventHistorySize = 256

type Daemon struct {
	mu           sync.Mutex
	sessions     map[string]ipc.AgentEvent
	legacy       map[string]ipc.Event
	history      []ipc.AgentEvent
	nextSequence uint64
	subscribers  map[chan ipc.AgentEvent]struct{}
}

func NewDaemon() *Daemon {
	return &Daemon{sessions: make(map[string]ipc.AgentEvent), legacy: make(map[string]ipc.Event), subscribers: make(map[chan ipc.AgentEvent]struct{})}
}

func (d *Daemon) publish(event ipc.AgentEvent) {
	d.mu.Lock()
	if event.Version == 0 {
		event.Version = 1
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	if event.Sequence == 0 {
		d.nextSequence++
		event.Sequence = d.nextSequence
	} else if event.Sequence > d.nextSequence {
		d.nextSequence = event.Sequence
	}
	d.sessions[event.SessionID] = event
	d.legacy[event.SessionID] = event.Legacy()
	d.history = append(d.history, event)
	if len(d.history) > eventHistorySize {
		d.history = d.history[len(d.history)-eventHistorySize:]
	}
	for ch := range d.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
	d.mu.Unlock()
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
	d.legacyEvent(event)
	w.WriteHeader(http.StatusOK)
}
func (d *Daemon) legacyEvent(event ipc.Event) {
	state := ipc.StateIdle
	switch event.Status {
	case ipc.StatusInitializing:
		state = ipc.StateStarting
	case ipc.StatusRunning:
		state = ipc.StateRunning
	case ipc.StatusWaiting:
		// The pre-v1 wrapper has only one ambiguous "Waiting" state. It is
		// commonly emitted for an ordinary ready prompt, so treating it as a
		// user-action request creates noisy false alerts. Only v1 classifiers
		// are allowed to publish input_required or permission_required.
		state = ipc.StateIdle
	case ipc.StatusFinished:
		state = ipc.StateCompleted
	case ipc.StatusError:
		state = ipc.StateFailed
	}
	d.publish(ipc.AgentEvent{Version: 1, SessionID: event.SessionID, Agent: event.AgentName, State: state, Confidence: 0.5, Summary: event.Message, Source: "legacy-event", PID: event.PID})
}
func (d *Daemon) handleV1Event(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var event ipc.AgentEvent
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil || event.SessionID == "" {
		http.Error(w, "invalid agent event", http.StatusBadRequest)
		return
	}
	d.publish(event)
	w.WriteHeader(http.StatusAccepted)
}
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return true
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || !errors.Is(err, syscall.ESRCH)
}
func (d *Daemon) cleanOrphans() {
	d.mu.Lock()
	var events []ipc.AgentEvent
	for id, s := range d.sessions {
		if s.State != ipc.StateCompleted && s.State != ipc.StateFailed && s.State != ipc.StateOrphaned && s.PID > 0 && !isProcessAlive(s.PID) {
			s.State = ipc.StateOrphaned
			s.Summary = "Wrapper process terminated"
			s.Source = "daemon-lifecycle"
			d.sessions[id] = s
			events = append(events, s)
		}
	}
	d.mu.Unlock()
	for _, event := range events {
		d.publish(event)
	}
}
func (d *Daemon) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", 405)
		return
	}
	d.cleanOrphans()
	d.mu.Lock()
	defer d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d.legacy)
}
func (d *Daemon) handleV1Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", 405)
		return
	}
	d.cleanOrphans()
	d.mu.Lock()
	defer d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d.sessions)
}

func (d *Daemon) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", 405)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch := make(chan ipc.AgentEvent, 32)
	last, _ := strconv.ParseUint(r.Header.Get("Last-Event-ID"), 10, 64)
	d.mu.Lock()
	replay := make([]ipc.AgentEvent, 0)
	for _, e := range d.history {
		if e.Sequence > last {
			replay = append(replay, e)
		}
	}
	d.subscribers[ch] = struct{}{}
	d.mu.Unlock()
	defer func() { d.mu.Lock(); delete(d.subscribers, ch); d.mu.Unlock() }()
	write := func(e ipc.AgentEvent) bool {
		data, err := json.Marshal(e)
		if err != nil {
			return false
		}
		if _, err = fmt.Fprintf(w, "event: state\nid: %d\ndata: %s\n\n", e.Sequence, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	for _, e := range replay {
		if !write(e) {
			return
		}
	}
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			if !write(e) {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func main() {
	d := NewDaemon()
	mux := http.NewServeMux()
	mux.HandleFunc("/event", d.handleEvent)
	mux.HandleFunc("/status", d.handleStatus)
	mux.HandleFunc("/v1/event", d.handleV1Event)
	mux.HandleFunc("/v1/status", d.handleV1Status)
	mux.HandleFunc("/v1/events", d.handleEvents)
	listener, err := net.Listen("tcp", ipc.ServerAddress)
	if err != nil {
		log.Fatalf("Failed to listen on TCP %s: %v", ipc.ServerAddress, err)
	}
	fmt.Printf("AgentWatch Daemon listening on http://%s\n", ipc.ServerAddress)
	if err := (&http.Server{Handler: mux}).Serve(listener); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
