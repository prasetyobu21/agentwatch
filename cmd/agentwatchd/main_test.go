package main

import (
	"testing"
	"time"

	"github.com/agentwatch/agentwatch/internal/ipc"
)

func TestCleanOrphansExpiresStaleActiveSession(t *testing.T) {
	d := NewDaemon()
	d.sessions["stale"] = ipc.AgentEvent{
		SessionID: "stale",
		Agent:     "codex",
		State:     ipc.StateRunning,
		Sequence:  4,
		Timestamp: time.Now().Add(-activeSessionStaleAfter - time.Second),
	}

	d.cleanOrphans()

	got := d.sessions["stale"]
	if got.State != ipc.StateOrphaned {
		t.Fatalf("state = %q, want %q", got.State, ipc.StateOrphaned)
	}
	if got.Sequence != 5 {
		t.Fatalf("sequence = %d, want 5", got.Sequence)
	}
}
