package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/FrexCheat/aether-code/internal/tui/components"
)

// scrollState is the history viewport's position: follow pins it to the newest
// line, otherwise top is the first visible line.
type scrollState struct {
	follow bool
	top    int
}

// transcriptView is a scrolling viewport over the recorded blocks. It holds no
// copy of the conversation: every line is the same block renderer the history
// uses, just with expanded bodies.
type transcriptView struct {
	open     bool
	frozen   int // len(blocks) when the view opened
	offset   int // lines scrolled up from the bottom
	rendered []string
	atWidth  int
	atFrozen int
}

// resolve returns the first visible line for a content of total lines in a
// viewport of the given height.
func (s scrollState) resolve(total, height int) int {
	maxTop := max(total-height, 0)
	if s.follow {
		return maxTop
	}
	return min(max(s.top, 0), maxTop)
}

// by scrolls delta lines, positive toward the newest. Reaching the bottom
// resumes following; content that fits entirely has nowhere to scroll, so it
// keeps following too.
func (s *scrollState) by(delta, total, height int) {
	maxTop := max(total-height, 0)
	if delta == 0 {
		return
	}
	if maxTop == 0 {
		*s = scrollState{follow: true}
		return
	}
	top := s.resolve(total, height) + delta
	if top >= maxTop {
		*s = scrollState{follow: true}
		return
	}
	*s = scrollState{top: max(top, 0)}
}

// scroll moves the transcript delta lines, positive toward the newest.
func (t *transcriptView) scroll(delta int) {
	t.offset = max(t.offset-delta, 0)
}

// inputOwner is a component that occupies the input region at the bottom of the
// window. At most one is active at a time; the composer is the fallback. A modal
// owner takes the whole region — the composer and its footer hide — and
// suspends the output region's live tail; a non-modal owner leaves the output
// region untouched.
type inputOwner interface {
	Modal() bool
	View(width int, footer components.FooterData) string
}

func (m Model) activeInput() inputOwner {
	if m.dialog.Active() {
		return m.dialog
	}
	return m.composer
}

// liveSuspended reports whether the output region's live tail must be hidden
// and its timer held, which is true while a modal input owner is up.
func (m Model) liveSuspended() bool {
	return m.activeInput().Modal()
}

// frame lays out the whole window: the history at the top, the bar (input box
// or dialog) directly below it, pinned to the bottom edge once the history
// fills the window.
func (m Model) frame() string {
	content, bar, viewHeight := m.layout()
	top := m.scroll.resolve(len(content), viewHeight)
	rows := make([]string, max(m.height, 1))
	clamp := lipgloss.NewStyle().MaxWidth(max(m.width, 1))
	for i := range viewHeight {
		rows[i] = clamp.Render(content[top+i])
	}
	for i, line := range bar {
		rows[viewHeight+i] = clamp.Render(line)
	}
	return strings.Join(rows, "\n")
}

// layout renders the content and the bar, and reports how many rows of content
// stay visible above the bar.
func (m Model) layout() (content, bar []string, viewHeight int) {
	height := max(m.height, 1)
	bar = m.inputLines()
	bar = bar[:min(len(bar), max(height-1, 1))]
	content = m.outputLines()
	viewHeight = max(min(len(content), height-len(bar)), 0)
	return content, bar, viewHeight
}

// outputLines is the output region: the scrollable history plus, unless a modal
// input owner has suspended it, the turn's live tail. It never renders an input
// component.
func (m Model) outputLines() []string {
	if m.liveSuspended() {
		return m.history
	}
	var live []string
	if m.compacting {
		live = []string{m.compactionSpinner.Line(m.clock.Frame(), "Compacting…")}
	} else {
		live = m.live.Lines(m.clock.Frame())
	}
	if len(live) == 0 {
		return m.history
	}
	lines := make([]string, 0, len(m.history)+len(live))
	lines = append(lines, m.history...)
	return append(lines, live...)
}

// inputLines is the input region: whichever owner is active renders it. A modal
// owner takes the whole region, so the composer and its footer step aside.
func (m Model) inputLines() []string {
	return strings.Split(m.activeInput().View(m.width, m.footer()), "\n")
}

func (m *Model) scrollBy(delta int) {
	content, _, viewHeight := m.layout()
	m.scroll.by(delta, len(content), viewHeight)
}

// pageLines is one screenful of history, kept short of the full viewport so the
// overlap makes it readable.
func (m Model) pageLines() int {
	_, _, viewHeight := m.layout()
	return max(viewHeight-1, 1)
}

// toggleTranscript opens the expanded view, frozen at the blocks recorded so
// far, or closes it again.
func (m *Model) toggleTranscript() {
	m.transcript.open = !m.transcript.open
	if m.transcript.open {
		m.transcript.frozen = len(m.blocks)
		m.transcript.offset = 0
	}
}

func (m Model) handleTranscriptKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m.quit()
	case "ctrl+o", "esc":
		m.toggleTranscript()
	case "up":
		m.transcript.scroll(-1)
	case "down":
		m.transcript.scroll(1)
	case "pgup":
		m.transcript.scroll(-m.pageLines())
	case "pgdown":
		m.transcript.scroll(m.pageLines())
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
	footer := components.Dim.Render("Showing detailed transcript · ctrl+o to toggle · ↑↓ scroll")
	return lipgloss.JoinVertical(lipgloss.Left, strings.Join(window, "\n"), footer)
}

func (m Model) transcriptContent() []string {
	var lines []string
	for i, b := range m.blocks[:m.transcript.frozen] {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, strings.Split(m.renderer.Render(b, true), "\n")...)
	}
	return lines
}
