package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/FrexCheat/aether-code/internal/tui/components"
)

// scrollState is the history viewport's position: follow pins it to the newest
// line, otherwise top is the first visible line.
type scrollState struct {
	follow bool
	top    int
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

func (m Model) handleTranscriptKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m.quit()
	case "ctrl+o", "esc":
		m.transcript.ToggleExpanded()
	case "up":
		m.transcript.Scroll(-1)
	case "down":
		m.transcript.Scroll(1)
	case "pgup":
		m.transcript.Scroll(-m.pageLines())
	case "pgdown":
		m.transcript.Scroll(m.pageLines())
	}
	return m, nil
}
