package ipc

import "time"

// AgentState is the normalized, user-facing state for a wrapped agent.
// Values are deliberately lower-case so the event API is stable across clients.
type AgentState string

const (
	StateStarting            AgentState = "starting"
	StateRunning             AgentState = "running"
	StateExecutingTool       AgentState = "executing_tool"
	StatePermissionRequired  AgentState = "permission_required"
	StateInputRequired       AgentState = "input_required"
	StatePermissionResolving AgentState = "permission_resolving"
	StateIdle                AgentState = "idle"
	StateCompleted           AgentState = "completed"
	StateFailed              AgentState = "failed"
	StateOrphaned            AgentState = "orphaned"
)

// AgentEvent is emitted by the wrapper and published by the local daemon.
// It intentionally contains no terminal content or raw input.
type AgentEvent struct {
	Version    int               `json:"version"`
	SessionID  string            `json:"sessionId"`
	Sequence   uint64            `json:"sequence"`
	Timestamp  time.Time         `json:"timestamp"`
	Agent      string            `json:"agent"`
	State      AgentState        `json:"state"`
	Confidence float64           `json:"confidence"`
	Summary    string            `json:"summary,omitempty"`
	Tool       string            `json:"tool,omitempty"`
	Source     string            `json:"source"`
	Evidence   []string          `json:"evidence,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	PID        int               `json:"pid,omitempty"`
}

// AgentStatus and Event retain the original /event and /status wire protocol.
// Keep these during the app migration so older AgentWatch.app bundles work.
type AgentStatus string

const (
	StatusRunning      AgentStatus = "Running"
	StatusInitializing AgentStatus = "Initializing"
	StatusWaiting      AgentStatus = "Waiting"
	StatusIdle         AgentStatus = "Idle"
	StatusFinished     AgentStatus = "Finished"
	StatusError        AgentStatus = "Error"
)

type Event struct {
	SessionID string      `json:"session_id"`
	AgentName string      `json:"agent_name"`
	Status    AgentStatus `json:"status"`
	Message   string      `json:"message,omitempty"`
	PID       int         `json:"pid,omitempty"`
}

func (e AgentEvent) Legacy() Event {
	status := StatusIdle
	switch e.State {
	case StateStarting:
		status = StatusInitializing
	case StateRunning, StateExecutingTool, StatePermissionResolving:
		status = StatusRunning
	case StatePermissionRequired, StateInputRequired:
		status = StatusWaiting
	case StateCompleted, StateOrphaned:
		status = StatusFinished
	case StateFailed:
		status = StatusError
	}
	return Event{SessionID: e.SessionID, AgentName: e.Agent, Status: status, Message: e.Summary, PID: e.PID}
}

const ServerAddress = "127.0.0.1:8765"
