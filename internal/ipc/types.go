package ipc

type AgentStatus string

const (
	StatusRunning  AgentStatus = "Running"
	StatusWaiting  AgentStatus = "Waiting"
	StatusIdle     AgentStatus = "Idle"
	StatusFinished AgentStatus = "Finished"
	StatusError    AgentStatus = "Error"
)

type Event struct {
	SessionID string      `json:"session_id"`
	AgentName string      `json:"agent_name"`
	Status    AgentStatus `json:"status"`
	Message   string      `json:"message,omitempty"`
}

const ServerAddress = "127.0.0.1:8765"
