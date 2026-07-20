package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/agentwatch/agentwatch/internal/ipc"
	"github.com/agentwatch/agentwatch/internal/terminal"
)

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

func TestClassifierDistinguishesPermissionAndInput(t *testing.T) {
	tests := []struct {
		name, output string
		want         ipc.AgentState
	}{
		{"permission", "Do you want to proceed?\nAllow once\nDeny", ipc.StatePermissionRequired},
		{"codex command permission", "Would you like to run the following command?\n1. Yes, proceed\n2. Yes, and don't ask again", ipc.StatePermissionRequired},
		{"tool permission", "Would you like to allow this tool?\nAlways allow\nDeny", ipc.StatePermissionRequired},
		{"ordinary question text", "Which environment should I use?", ipc.StateRunning},
		{"ordinary request text", "Please provide a helper that validates input", ipc.StateRunning},
		{"interactive question", "Which environment should I use?\n  1. Production\n  2. Staging\nEnter to select · ↑/↓ to navigate", ipc.StateInputRequired},
		{"interactive question with prompt marker", "Which environment should I use?\n❯ 1. Production\n  2. Staging\nEnter to select · ↑/↓ to navigate", ipc.StateInputRequired},
		{"tool", "Executing rg --files", ipc.StateExecutingTool},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := terminal.New(100, 10)
			_ = m.Write([]byte(test.output))
			pw := &ParserWriter{AgentName: "codex", terminal: m, outputBuffer: []byte(test.output)}
			if got := pw.classifyLocked(); got != test.want {
				t.Fatalf("classifyLocked() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPromptSubmissionEnterIsNotApproval(t *testing.T) {
	pw := &ParserWriter{
		AgentName:    "codex",
		outputBuffer: []byte("Would you like to run the following command?\nYes, proceed"),
		lastState:    ipc.StateRunning,
		recentInput:  []inputRecord{{kind: "enter", at: time.Now()}},
	}
	if got := pw.classifyLocked(); got != ipc.StatePermissionRequired {
		t.Fatalf("classifyLocked() = %q, want %q", got, ipc.StatePermissionRequired)
	}
}

func TestNonCodexComposerIsNotTreatedAsIdle(t *testing.T) {
	pw := &ParserWriter{AgentName: "claude", outputBuffer: []byte("› Ask Codex anything")}
	if pw.isCurrentlyIdleLocked() {
		t.Fatal("Codex composer marker must not affect other agents")
	}
}

func TestClaudePromptWinsOverOlderActivityOnScreen(t *testing.T) {
	m := terminal.New(100, 10)
	output := "✻ Working…\nCompleted a change\n❯"
	if err := m.Write([]byte(output)); err != nil {
		t.Fatal(err)
	}
	pw := &ParserWriter{AgentName: "claude", terminal: m, outputBuffer: []byte(output)}
	if got := pw.classifyLocked(); got != ipc.StateIdle {
		t.Fatalf("classifyLocked() = %q, want %q", got, ipc.StateIdle)
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

func TestCodexReadyScreenOverridesStaleBusyTitle(t *testing.T) {
	m := terminal.New(100, 10)
	output := "\x1b]0;⠴ agentwatch\x07\n› Ask Codex anything\n? for shortcuts"
	if err := m.Write([]byte(output)); err != nil {
		t.Fatal(err)
	}
	pw := &ParserWriter{AgentName: "codex", terminal: m, outputBuffer: []byte(output)}
	pw.updateCodexTitleLocked()
	if got := pw.classifyLocked(); got != ipc.StateIdle {
		t.Fatalf("classifyLocked() = %q, want %q", got, ipc.StateIdle)
	}
}

func TestAmbiguousFocusRedrawKeepsCodexIdle(t *testing.T) {
	m := terminal.New(100, 10)
	output := "\x1b]0;⠴ agentwatch\x07\n› Ask Codex anything\n? for shortcuts"
	if err := m.Write([]byte(output)); err != nil {
		t.Fatal(err)
	}
	pw := &ParserWriter{
		AgentName:    "codex",
		terminal:     m,
		outputBuffer: []byte(output),
		lastState:    ipc.StateIdle,
	}
	pw.updateCodexTitleLocked()

	// Losing terminal focus can repaint the screen without the ready footer or
	// a new title. The old spinner title must not reactivate an idle session.
	pw.terminal = terminal.New(100, 10)
	redraw := "agentwatch workspace"
	if err := pw.terminal.Write([]byte(redraw)); err != nil {
		t.Fatal(err)
	}
	pw.outputBuffer = append(pw.outputBuffer, []byte(redraw)...)
	pw.updateCodexTitleLocked()

	if got := pw.classifyLocked(); got != ipc.StateIdle {
		t.Fatalf("classifyLocked() = %q, want %q", got, ipc.StateIdle)
	}
}

func TestFreshCodexBusyTitleStartsRunningFromIdle(t *testing.T) {
	pw := &ParserWriter{AgentName: "codex", lastState: ipc.StateIdle}
	pw.outputBuffer = []byte("\x1b]0;agentwatch\x07")
	pw.updateCodexTitleLocked()
	pw.outputBuffer = []byte("\x1b]0;⠴ agentwatch\x07")
	pw.updateCodexTitleLocked()

	if got := pw.classifyLocked(); got != ipc.StateRunning {
		t.Fatalf("classifyLocked() = %q, want %q", got, ipc.StateRunning)
	}
}

func TestNormalizeFocusInputKeepsAgentTUIFocused(t *testing.T) {
	tests := []struct {
		name          string
		input         []byte
		wantForwarded []byte
		wantUserInput []byte
	}{
		{
			name:          "focus lost becomes focus gained",
			input:         []byte("\x1b[O"),
			wantForwarded: []byte("\x1b[I"),
		},
		{
			name:          "focus gained is not user input",
			input:         []byte("\x1b[I"),
			wantForwarded: []byte("\x1b[I"),
		},
		{
			name:          "mixed input preserves typed bytes",
			input:         []byte("a\x1b[Ob"),
			wantForwarded: []byte("a\x1b[Ib"),
			wantUserInput: []byte("ab"),
		},
		{
			name:          "other escape sequence is untouched",
			input:         []byte("\x1b[A"),
			wantForwarded: []byte("\x1b[A"),
			wantUserInput: []byte("\x1b[A"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			forwarded, userInput := normalizeFocusInput(test.input)
			if !bytes.Equal(forwarded, test.wantForwarded) {
				t.Fatalf("forwarded = %q, want %q", forwarded, test.wantForwarded)
			}
			if !bytes.Equal(userInput, test.wantUserInput) {
				t.Fatalf("userInput = %q, want %q", userInput, test.wantUserInput)
			}
		})
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
