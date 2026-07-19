package main

import "testing"

func TestCodexIdleDetection(t *testing.T) {
	tests := []struct {
		name   string
		output string
		idle   bool
	}{
		{name: "composer", output: "\x1b[2K› Ask Codex anything", idle: true},
		{name: "working", output: "› previous request\n• Working (12s · esc to interrupt)", idle: false},
		{name: "composer after working", output: "• Working (12s · esc to interrupt)\n› Ask Codex anything", idle: true},
		{name: "ready footer after working", output: "• Working (12s · esc to interrupt)\n? for shortcuts", idle: true},
		{name: "tool execution", output: "• Executing rg --files", idle: false},
		{name: "approval", output: "Do you want to proceed?\n  Allow once\n  Deny", idle: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pw := &ParserWriter{AgentName: "codex", outputBuffer: []byte(test.output)}
			if got := pw.isCurrentlyIdleLocked(); got != test.idle {
				t.Fatalf("isCurrentlyIdleLocked() = %v, want %v", got, test.idle)
			}
		})
	}
}

func TestNonCodexComposerIsNotTreatedAsIdle(t *testing.T) {
	pw := &ParserWriter{AgentName: "claude", outputBuffer: []byte("› Ask Codex anything")}
	if pw.isCurrentlyIdleLocked() {
		t.Fatal("Codex composer marker must not affect other agents")
	}
}

func TestCodexTerminalTitleControlsState(t *testing.T) {
	pw := &ParserWriter{AgentName: "codex"}

	pw.outputBuffer = []byte("\x1b]0;⠴ agentwatch\x07")
	pw.updateCodexTitleLocked()
	if pw.isCurrentlyIdleLocked() {
		t.Fatal("Codex spinner title must be running")
	}

	pw.outputBuffer = []byte("\x1b]0;agentwatch\x07")
	pw.updateCodexTitleLocked()
	if !pw.isCurrentlyIdleLocked() {
		t.Fatal("plain Codex title must be waiting")
	}
}

func TestIsCodexInteractive(t *testing.T) {
	tests := []struct {
		name      string
		agentName string
		args      []string
		want      bool
	}{
		{name: "root command", agentName: "codex", want: true},
		{name: "root command with model", agentName: "codex", args: []string{"--model", "gpt-5"}, want: true},
		{name: "resume command", agentName: "codex", args: []string{"resume"}, want: true},
		{name: "exec command", agentName: "codex", args: []string{"exec", "fix it"}, want: false},
		{name: "other agent", agentName: "claude", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isCodexInteractive(test.agentName, test.args); got != test.want {
				t.Fatalf("isCodexInteractive() = %v, want %v", got, test.want)
			}
		})
	}
}
