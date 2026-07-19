// Package terminal contains a small, defensive terminal screen model. It is
// intentionally independent from classification so it can be replaced by a
// fuller VT parser later without changing the wrapper protocol.
package terminal

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type ScreenSnapshot struct {
	Lines     []string  `json:"lines"`
	CursorRow int       `json:"cursorRow"`
	CursorCol int       `json:"cursorCol"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	AltScreen bool      `json:"altScreen"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Model struct {
	mu                      sync.Mutex
	width, height, row, col int
	lines                   [][]rune
	altLines                [][]rune
	alt                     bool
	updated                 time.Time
}

func New(width, height int) *Model {
	m := &Model{}
	m.Resize(width, height)
	return m
}

func (m *Model) Resize(width, height int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	m.width, m.height = width, height
	m.ensureRows()
}

func (m *Model) ensureRows() {
	for len(m.lines) < m.height {
		m.lines = append(m.lines, []rune{})
	}
}

func (m *Model) active() *[][]rune {
	if m.alt {
		return &m.altLines
	}
	return &m.lines
}

func (m *Model) rowLine() *[]rune {
	lines := m.active()
	for len(*lines) <= m.row {
		*lines = append(*lines, []rune{})
	}
	return &(*lines)[m.row]
}

func (m *Model) Write(data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; i < len(data); {
		if data[i] == 0x1b {
			consumed := m.escape(data[i:])
			if consumed > 0 {
				i += consumed
				continue
			}
		}
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		i += size
		switch r {
		case '\r':
			m.col = 0
		case '\n':
			m.newline()
		case '\b':
			if m.col > 0 {
				m.col--
			}
		default:
			if r >= 0x20 {
				m.put(r)
			}
		}
	}
	m.updated = time.Now()
	return nil
}

func (m *Model) put(r rune) {
	if m.col >= m.width {
		m.newline()
	}
	line := m.rowLine()
	for len(*line) <= m.col {
		*line = append(*line, ' ')
	}
	(*line)[m.col] = r
	m.col++
}
func (m *Model) newline() {
	m.row++
	m.col = 0
	if m.row >= m.height {
		lines := m.active()
		if len(*lines) > 0 {
			*lines = (*lines)[1:]
		}
		*lines = append(*lines, []rune{})
		m.row = m.height - 1
	}
}

// escape handles the common CSI/OSC sequences used by coding-agent TUIs. Bad
// sequences are ignored; terminal forwarding is never affected by this parser.
func (m *Model) escape(b []byte) int {
	if len(b) < 2 {
		return 1
	}
	if b[1] == ']' {
		for i := 2; i < len(b); i++ {
			if b[i] == 7 {
				return i + 1
			}
			if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
				return i + 2
			}
		}
		return len(b)
	}
	if b[1] != '[' {
		return 2
	}
	i := 2
	for i < len(b) && (b[i] < 0x40 || b[i] > 0x7e) {
		i++
	}
	if i == len(b) {
		return len(b)
	}
	params, final := string(b[2:i]), b[i]
	parts := strings.Split(strings.TrimPrefix(params, "?"), ";")
	n := func(index, fallback int) int {
		if index >= len(parts) || parts[index] == "" {
			return fallback
		}
		v := 0
		for _, c := range parts[index] {
			if c < '0' || c > '9' {
				return fallback
			}
			v = v*10 + int(c-'0')
		}
		return v
	}
	switch final {
	case 'A':
		m.row -= n(0, 1)
		if m.row < 0 {
			m.row = 0
		}
	case 'B':
		m.row += n(0, 1)
		if m.row >= m.height {
			m.row = m.height - 1
		}
	case 'C':
		m.col += n(0, 1)
		if m.col >= m.width {
			m.col = m.width - 1
		}
	case 'D':
		m.col -= n(0, 1)
		if m.col < 0 {
			m.col = 0
		}
	case 'H', 'f':
		m.row = n(0, 1) - 1
		m.col = n(1, 1) - 1
		if m.row < 0 {
			m.row = 0
		}
		if m.col < 0 {
			m.col = 0
		}
	case 'K':
		line := m.rowLine()
		mode := n(0, 0)
		if mode == 2 {
			*line = []rune{}
			m.col = 0
		} else if mode == 0 && m.col < len(*line) {
			*line = (*line)[:m.col]
		}
	case 'J':
		if n(0, 0) == 2 {
			*m.active() = make([][]rune, m.height)
			m.row, m.col = 0, 0
		}
	case 'h':
		if strings.HasPrefix(params, "?1049") || strings.HasPrefix(params, "?1047") {
			m.alt = true
			m.altLines = make([][]rune, m.height)
			m.row, m.col = 0, 0
		}
	case 'l':
		if strings.HasPrefix(params, "?1049") || strings.HasPrefix(params, "?1047") {
			m.alt = false
			m.row, m.col = 0, 0
		}
	}
	return i + 1
}

func (m *Model) Snapshot() ScreenSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	lines := *m.active()
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = strings.TrimRight(string(line), " ")
	}
	return ScreenSnapshot{Lines: out, CursorRow: m.row, CursorCol: m.col, Width: m.width, Height: m.height, AltScreen: m.alt, UpdatedAt: m.updated}
}
