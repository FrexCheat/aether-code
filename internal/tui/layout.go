package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/FrexCheat/aether-code/internal/tui/components"
)

// The window's tuning knobs, gathered so a layout tweak is one stop. A widget's
// own defaults (maxRunningTools, maxPermissionRules, frameInterval) stay inside
// components, which cannot import this package.
const (
	// wheelLines is one wheel notch, in history lines.
	wheelLines = 3
	// collapseLines is how many result lines a collapsed tool block shows.
	collapseLines = 3
	// titleClampChars caps a tool title's displayed rune length.
	titleClampChars = 160
	// maxInputRows caps how tall the input box grows with its content.
	maxInputRows = 8
	// bannerHint is the banner's one-line hint. It deliberately does not
	// enumerate the commands: with seven of them the hint outgrew the space
	// beside the logo, which the banner then silently dropped. Tab completion
	// and /help are the discovery path.
	bannerHint = "Press Enter to send · shift+enter for a newline · / for commands"
)

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
	history := m.transcript.Lines()
	if m.liveSuspended() {
		return history
	}
	var live []string
	if m.compacting {
		live = []string{components.Dots.Line(m.clock.Frame(), "Compacting…")}
	} else {
		live = m.live.Lines(m.clock.Frame())
	}
	if len(live) == 0 {
		return history
	}
	lines := make([]string, 0, len(history)+len(live))
	lines = append(lines, history...)
	return append(lines, live...)
}

// inputLines is the input region: whichever owner is active renders it. A modal
// owner takes the whole region, so the composer and its footer step aside.
func (m Model) inputLines() []string {
	return strings.Split(m.activeInput().View(m.width, m.footer()), "\n")
}
