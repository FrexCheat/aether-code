package tui

import (
	"strings"

	"github.com/FrexCheat/aether-code/internal/tui/components"
)

type transcript struct {
	renderer blockRenderer
	blocks   []block
	lines    []string // rendered scrollback, rebuilt on width change
	expanded expandedView
}

type expandedView struct {
	open     bool
	frozen   int
	offset   int
	rendered []string
	atWidth  int
	atFrozen int
}

func (t *transcript) SetWidth(w int) { t.renderer.SetWidth(w) }

func (t *transcript) Load(blocks []block) { t.blocks = blocks }

func (t *transcript) Emit(b block) {
	t.blocks = append(t.blocks, b)
	t.lines = append(t.lines, t.blockLines(b)...)
}

func (t *transcript) Clear() {
	t.blocks = nil
	t.lines = nil
}

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

func (t *transcript) Lines() []string { return t.lines }

func (t transcript) blockLines(b block) []string {
	lines := hardWrap(t.renderer.Render(b, false), t.renderer.width)
	return append(lines, "")
}

func (t transcript) IsExpanded() bool { return t.expanded.open }

func (t *transcript) ToggleExpanded() {
	t.expanded.open = !t.expanded.open
	if t.expanded.open {
		t.expanded.frozen = len(t.blocks)
		t.expanded.offset = 0
	}
}

func (t *transcript) Scroll(delta int) {
	t.expanded.offset = max(t.expanded.offset-delta, 0)
}

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
