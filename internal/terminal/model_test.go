package terminal

import "testing"

func TestModelUsesVisibleScreenNotRedrawHistory(t *testing.T) {
	m := New(40, 4)
	if err := m.Write([]byte("Working...\r\x1b[2KReady >")); err != nil {
		t.Fatal(err)
	}
	s := m.Snapshot()
	if got := s.Lines[0]; got != "Ready >" {
		t.Fatalf("visible line = %q, want %q", got, "Ready >")
	}
}

func TestModelAlternateScreen(t *testing.T) {
	m := New(40, 4)
	_ = m.Write([]byte("main\x1b[?1049halt\x1b[?1049l"))
	if m.Snapshot().AltScreen {
		t.Fatal("alternate screen should be restored")
	}
	if got := m.Snapshot().Lines[0]; got != "main" {
		t.Fatalf("main buffer = %q", got)
	}
}
