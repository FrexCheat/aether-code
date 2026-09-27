package tui

import (
	"strings"

	"github.com/FrexCheat/aether-code/internal/tui/components"
)

// transcript is the window's static output region: every block printed so far,
// re-rendered at the current width, plus the ctrl+o expanded view. It owns the
// one blockRenderer, so the scrollback and the expanded view can never diverge
// in style. It holds no copy of the conversation — blocks in, lines out.
type transcript struct {
	renderer blockRenderer
	blocks   []block
	lines    []string // rendered scrollback, rebuilt on width change
	expanded expandedView
}

// expandedView freezes the transcript when ctrl+o opens it, so later output
// does not shift what is on screen.
type expandedView struct {
	open     bool
	frozen   int
	offset   int
	rendered []string
	atWidth  int
	atFrozen int
}

// SetWidth rebuilds the markdown renderer against a new terminal width.
func (t *transcript) SetWidth(w int) { t.renderer.SetWidth(w) }

// Load seeds the transcript with blocks reconstructed from a restored history.
func (t *transcript) Load(blocks []block) { t.blocks = blocks }

// Emit records a block and renders it into the scrollback.
func (t *transcript) Emit(b block) {
	t.blocks = append(t.blocks, b)
	t.lines = append(t.lines, t.blockLines(b)...)
}

// Clear drops every block and rendered line; the caller rebuilds the banner.
func (t *transcript) Clear() {
	t.blocks = nil
	t.lines = nil
}

// Rebuild re-renders the banner and every recorded block at the current width.
func (t *transcript) Rebuild(version, workDir, model string) {
	t.lines = make([]string, 0, len(t.lines)+1)
	if t.renderer.width > 0 {
		banner := t.renderer.Banner(version, workDir, model, bannerHint)
		t.lines = append(t.lines, hardWrap(banner, t.renderer.width)...)
		t.lines = append(t.lines, "")
	}
	for _, b := range t.blocks {
		t.lines = append(t.lines, t.blockLines(b)...)
	}
}

// Lines is the scrollback content.
func (t *transcript) Lines() []string { return t.lines }

// blockLines renders one block for the scrollback, followed by a blank
// separator.
func (t transcript) blockLines(b block) []string {
	lines := hardWrap(t.renderer.Render(b, false), t.renderer.width)
	return append(lines, "")
}

// IsExpanded reports whether the ctrl+o view is open.
func (t transcript) IsExpanded() bool { return t.expanded.open }

// ToggleExpanded opens the expanded view frozen at the blocks recorded so far,
// or closes it again.
func (t *transcript) ToggleExpanded() {
	t.expanded.open = !t.expanded.open
	if t.expanded.open {
		t.expanded.frozen = len(t.blocks)
		t.expanded.offset = 0
	}
}

// Scroll moves the expanded view delta lines, positive toward the newest.
func (t *transcript) Scroll(delta int) {
	t.expanded.offset = max(t.expanded.offset-delta, 0)
}

// Screen renders the expanded view, windowed to height, with its footer hint.
func (t *transcript) Screen(width, height int) string {
	if t.expanded.atWidth != width || t.expanded.atFrozen != t.expanded.frozen {
		t.expanded.rendered = t.content()
		t.expanded.atWidth = width
		t.expanded.atFrozen = t.expanded.frozen
	}
	visible := max(height-1, 1)
	total := len(t.expanded.rendered)
	t.expanded.offset = min(t.expanded.offset, max(total-visible, 0))
	start := max(total-visible, 0) - t.expanded.offset
	window := t.expanded.rendered[start:min(start+visible, total)]

	lines := make([]string, 0, visible+1)
	for range visible - len(window) {
		lines = append(lines, "")
	}
	lines = append(lines, window...)
	lines = append(lines, components.Dim.Render("Showing detailed transcript · ctrl+o to toggle · ↑↓ scroll"))
	return strings.Join(lines, "\n")
}

// content renders every frozen block in full, blank-separated.
func (t transcript) content() []string {
	var lines []string
	for i, b := range t.blocks[:t.expanded.frozen] {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, strings.Split(t.renderer.Render(b, true), "\n")...)
	}
	return lines
}
