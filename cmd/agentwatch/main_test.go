package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentwatch/agentwatch/internal/ipc"
	"github.com/agentwatch/agentwatch/internal/terminal"
)

func TestHookEventMapping(t *testing.T) {
	tests := []struct {
		name  string
		input hookInput
		state ipc.AgentState
		ok    bool
	}{
		{name: "start", input: hookInput{SessionID: "one", HookEventName: "SessionStart"}, state: ipc.StateIdle, ok: true},
		{name: "tool", input: hookInput{SessionID: "one", HookEventName: "PreToolUse", ToolName: "Bash"}, state: ipc.StateExecutingTool, ok: true},
		{name: "question", input: hookInput{SessionID: "one", HookEventName: "PreToolUse", ToolName: "AskUserQuestion"}, state: ipc.StateInputRequired, ok: true},
		{name: "permission", input: hookInput{SessionID: "one", HookEventName: "PermissionRequest"}, state: ipc.StatePermissionRequired, ok: true},
		{name: "idle notification", input: hookInput{SessionID: "one", HookEventName: "Notification", NotificationType: "idle_prompt"}, state: ipc.StateIdle, ok: true},
		{name: "input notification", input: hookInput{SessionID: "one", HookEventName: "Notification", NotificationType: "agent_needs_input"}, state: ipc.StateInputRequired, ok: true},
		{name: "stop", input: hookInput{SessionID: "one", HookEventName: "Stop"}, state: ipc.StateIdle, ok: true},
		{name: "unknown", input: hookInput{SessionID: "one", HookEventName: "Other"}},
		{name: "missing session", input: hookInput{HookEventName: "Stop"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event, ok := hookEvent("claude", test.input)
			if ok != test.ok || event.State != test.state {
				t.Fatalf("hookEvent() = (%q, %v), want (%q, %v)", event.State, ok, test.state, test.ok)
			}
			if ok && (event.SessionID != "claude:one" || event.PID != 0) {
				t.Fatalf("event = %#v", event)
			}
		})
	}
}

func TestAgyHookMappingAndResponses(t *testing.T) {
	tests := []struct {
		event    string
		input    hookInput
		state    ipc.AgentState
		response string
	}{
		{event: "PreInvocation", input: hookInput{SessionID: "one", HookEventName: "PreInvocation"}, state: ipc.StateRunning, response: `{}`},
		{event: "PostToolUse", input: hookInput{SessionID: "one", HookEventName: "PostToolUse", ToolName: "run_command"}, state: ipc.StateRunning, response: `{}`},
		{event: "Stop", input: hookInput{SessionID: "one", HookEventName: "Stop"}, state: ipc.StateIdle, response: `{"decision":"stop"}`},
		{event: "Stop", input: hookInput{SessionID: "one", HookEventName: "Stop", TerminationReason: "error"}, state: ipc.StateFailed, response: `{"decision":"stop"}`},
	}
	for _, test := range tests {
		event, ok := hookEvent("agy", test.input)
		if !ok || event.State != test.state {
			t.Fatalf("%s = (%q, %v), want %q", test.event, event.State, ok, test.state)
		}
		if got := hookResponse("agy", test.event); got != test.response {
			t.Fatalf("%s response = %q, want %q", test.event, got, test.response)
		}
	}
}

func TestAgyPluginIsObservationOnly(t *testing.T) {
	manifest, hooks, err := agyPluginFiles("/tmp/aw")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(manifest) || !json.Valid(hooks) {
		t.Fatal("invalid plugin JSON")
	}
	for _, event := range []string{"PreInvocation", "PostToolUse", "Stop"} {
		if !bytes.Contains(hooks, []byte(event)) {
			t.Fatalf("missing %s", event)
		}
	}
	if bytes.Contains(hooks, []byte("PreToolUse")) {
		t.Fatal("agy plugin must never participate in tool permission decisions")
	}
	if !bytes.Contains(hooks, []byte("'/tmp/aw' hook agy")) {
		t.Fatal("missing AgentWatch relay command")
	}
}

func TestInstallHooksPreservesSettingsAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := []byte("{\n  \"permissions\": {\"allow\": [\"Read\"]},\n  \"hooks\": {\"Stop\": [{\"hooks\": [{\"type\": \"command\", \"command\": \"existing\"}]}]}\n}\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	backup, err := installHooks(path, "claude", "/tmp/aw")
	if err != nil {
		t.Fatal(err)
	}
	if backup == "" {
		t.Fatal("expected backup")
	}
	backupData, err := os.ReadFile(backup)
	if err != nil || !bytes.Equal(backupData, original) {
		t.Fatalf("backup changed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	if root["permissions"] == nil {
		t.Fatal("existing settings were removed")
	}
	hooks := root["hooks"].(map[string]any)
	if got := len(hooks["Stop"].([]any)); got != 2 {
		t.Fatalf("Stop hook count = %d, want 2", got)
	}
	if _, err := installHooks(path, "claude", "/tmp/aw"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	json.Unmarshal(data, &root)
	hooks = root["hooks"].(map[string]any)
	if got := len(hooks["Stop"].([]any)); got != 2 {
		t.Fatalf("idempotent Stop hook count = %d, want 2", got)
	}
}

func TestInstallHooksRefusesInvalidSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := installHooks(path, "claude", "/tmp/aw"); err == nil {
		t.Fatal("expected invalid JSON error")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "not json" {
		t.Fatal("invalid settings file was modified")
	}
}

func TestInstallHooksDoesNotClaimBackupForNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	backup, err := installHooks(path, "codex", "/tmp/aw")
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Fatalf("backup = %q for new file", backup)
	}
}

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

func TestClaudeInterruptIndicatorKeepsRunning(t *testing.T) {
	m := terminal.New(100, 10)
	output := "✻ Working… · esc to interrupt\nline 1\nline 2\nline 3\nline 4\nline 5\n⏵⏵ auto mode on · ← for agents"
	if err := m.Write([]byte(output)); err != nil {
		t.Fatal(err)
	}
	pw := &ParserWriter{AgentName: "claude", terminal: m, outputBuffer: []byte(output)}
	if got := pw.classifyLocked(); got != ipc.StateRunning {
		t.Fatalf("classifyLocked() = %q, want %q", got, ipc.StateRunning)
	}
}

func TestClaudeInterruptDisappearingMarksIdle(t *testing.T) {
	m := terminal.New(100, 10)
	output := "✻ Working…\n❯ Try \"fix lint errors\""
	if err := m.Write([]byte(output)); err != nil {
		t.Fatal(err)
	}
	pw := &ParserWriter{AgentName: "claude", terminal: m, outputBuffer: []byte(output), claudeBusySeen: true}
	if got := pw.classifyLocked(); got != ipc.StateIdle {
		t.Fatalf("classifyLocked() = %q, want %q", got, ipc.StateIdle)
	}
}

func TestClaudeOldPermissionTextDoesNotTriggerAlert(t *testing.T) {
	m := terminal.New(100, 10)
	output := "Allow once\nline 1\nline 2\nline 3\nline 4\nline 5\n✻ Working… · esc to interrupt"
	if err := m.Write([]byte(output)); err != nil {
		t.Fatal(err)
	}
	pw := &ParserWriter{AgentName: "claude", terminal: m, outputBuffer: []byte(output)}
	if got := pw.classifyLocked(); got != ipc.StateRunning {
		t.Fatalf("classifyLocked() = %q, want %q", got, ipc.StateRunning)
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

func TestCodexBusyTitleOverridesStaleReadyScreen(t *testing.T) {
	m := terminal.New(100, 10)
	output := "\x1b]0;⠴ agentwatch\x07\n› Ask Codex anything\n? for shortcuts"
	if err := m.Write([]byte(output)); err != nil {
		t.Fatal(err)
	}
	pw := &ParserWriter{AgentName: "codex", terminal: m, outputBuffer: []byte(output)}
	pw.updateCodexTitleLocked()
	if got := pw.classifyLocked(); got != ipc.StateRunning {
		t.Fatalf("classifyLocked() = %q, want %q", got, ipc.StateRunning)
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

func TestFinalStateWaitsForDelivery(t *testing.T) {
	deliveryStarted := make(chan struct{})
	releaseDelivery := make(chan struct{})
	returned := make(chan struct{})
	pw := &ParserWriter{
		SessionID: "claude-1",
		AgentName: "claude",
		lastState: ipc.StateRunning,
		deliver: func(event ipc.AgentEvent) {
			if event.State != ipc.StateCompleted {
				t.Errorf("state = %q, want %q", event.State, ipc.StateCompleted)
			}
			close(deliveryStarted)
			<-releaseDelivery
		},
	}

	go func() {
		pw.setFinalStateWithSummary(ipc.StateCompleted, "Completed", "process-lifecycle")
		close(returned)
	}()

	<-deliveryStarted
	select {
	case <-returned:
		t.Fatal("setFinalStateWithSummary returned before delivery completed")
	default:
	}
	close(releaseDelivery)
	<-returned
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
