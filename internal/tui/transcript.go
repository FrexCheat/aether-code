package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// transcriptItem is one printed block, recorded at print time with its full
// uncollapsed text. The scrollback shows collapsed bodies; the ctrl+o
// overview re-renders from here.
type transcriptItem struct {
	kind   string // user | assistant | tool | notice
	title  string
	body   string
	isErr  bool
	isDiff bool
}

type transcriptView struct {
	items []transcriptItem
	open  bool
	// Entry freezes the snapshot (claude-code's frozenTranscriptState): the
	// rendered text is cached until the width or the snapshot changes, so
	// scrolling never re-runs glamour.
	frozen   int
	offset   int // lines scrolled up from the bottom
	rendered []string
	atWidth  int
	atFrozen int
}

func (m *Model) record(item transcriptItem) {
	m.transcript.items = append(m.transcript.items, item)
}

func (m Model) toggleTranscript() (tea.Model, tea.Cmd) {
	m.transcript.open = !m.transcript.open
	if m.transcript.open {
		m.transcript.frozen = len(m.transcript.items)
		m.transcript.offset = 0
	}
	return m, nil
}

func (m Model) handleTranscriptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m.quit()
	case "ctrl+o", "esc":
		m.transcript.open = false
	case "up":
		m.transcript.offset++
	case "down":
		m.transcript.offset = max(m.transcript.offset-1, 0)
	case "pgup":
		m.transcript.offset += max(m.height-3, 1)
	case "pgdown":
		m.transcript.offset = max(m.transcript.offset-max(m.height-3, 1), 0)
	}
	return m, nil
}

func (m Model) transcriptScreen() string {
	if m.transcript.atWidth != m.width || m.transcript.atFrozen != m.transcript.frozen {
		m.transcript.rendered = m.transcriptContent()
		m.transcript.atWidth = m.width
		m.transcript.atFrozen = m.transcript.frozen
	}
	height := max(m.height-1, 1)
	total := len(m.transcript.rendered)
	if m.transcript.offset > max(total-height, 0) {
		m.transcript.offset = max(total-height, 0)
	}
	start := max(total-height, 0) - m.transcript.offset
	end := min(start+height, total)
	window := m.transcript.rendered[start:end]
	if pad := height - len(window); pad > 0 {
		window = append(make([]string, pad), window...)
	}
	footer := styleDim.Render("Showing detailed transcript · ctrl+o to toggle · ↑↓ scroll")
	return lipgloss.JoinVertical(lipgloss.Left, strings.Join(window, "\n"), footer)
}

func (m Model) transcriptContent() []string {
	c := m.components()
	var lines []string
	for i, it := range m.transcript.items[:m.transcript.frozen] {
		if i > 0 {
			lines = append(lines, "")
		}
		var block string
		switch it.kind {
		case "user":
			block = c.user(it.body)
		case "assistant":
			block = c.assistant(it.body)
		case "tool":
			block = c.transcriptTool(it)
		case "notice":
			block = c.notice(it.body, noticeSeverityFor(it))
		}
		lines = append(lines, strings.Split(block, "\n")...)
	}
	return lines
}

func noticeSeverityFor(it transcriptItem) noticeSeverity {
	if it.isErr {
		return noticeError
	}
	return noticeInfo
}

func (c components) transcriptTool(it transcriptItem) string {
	dot := styleToolDone.Render("●")
	if it.isErr {
		dot = styleToolFailed.Render("●")
	}
	var b strings.Builder
	b.WriteString(toolIndent + dot + " " + it.title)
	if it.body != "" {
		for line := range strings.SplitSeq(it.body, "\n") {
			b.WriteString("\n" + toolIndent)
			switch {
			case it.isDiff:
				b.WriteString(colorDiffLine(line))
			case it.isErr:
				b.WriteString(styleError.Render(line))
			default:
				b.WriteString(line)
			}
		}
	}
	return b.String()
}
