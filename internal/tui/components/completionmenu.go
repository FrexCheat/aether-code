package components

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// CompletionOption is one candidate in the completion menu: the command name
// without its slash, its argument hint and the description shown beside them.
type CompletionOption struct {
	Name        string
	Hint        string
	Description string
}

// CompletionMenu is the Tab-completion candidate list. It lives inside the
// composer, below the text box, and never takes the box's place.
type CompletionMenu struct {
	options []CompletionOption
	cursor  int
}

func (m CompletionMenu) Active() bool { return len(m.options) > 0 }

func (m *CompletionMenu) Open(options []CompletionOption) {
	m.options = options
	m.cursor = 0
}

func (m *CompletionMenu) Close() {
	m.options = nil
	m.cursor = 0
}

func (m *CompletionMenu) Move(delta int) {
	if len(m.options) == 0 {
		return
	}
	m.cursor = (m.cursor + delta + len(m.options)) % len(m.options)
}

func (m CompletionMenu) Selected() (CompletionOption, bool) {
	if len(m.options) == 0 {
		return CompletionOption{}, false
	}
	return m.options[m.cursor], true
}

// View renders one line per candidate, the cursor row marked and the names in
// a shared column.
func (m CompletionMenu) View(width int) string {
	labels := make([]string, len(m.options))
	nameWidth := 0
	for i, opt := range m.options {
		labels[i] = "/" + opt.Name
		if opt.Hint != "" {
			labels[i] += " " + opt.Hint
		}
		if n := lipgloss.Width(labels[i]); n > nameWidth {
			nameWidth = n
		}
	}
	lines := make([]string, len(m.options))
	for i, opt := range m.options {
		cursor, label := "  ", styleMenuName.Render(labels[i])
		if i == m.cursor {
			cursor, label = styleMenuCursor.Render("› "), styleMenuCursor.Render(labels[i])
		}
		line := cursor + label + strings.Repeat(" ", nameWidth-lipgloss.Width(labels[i]))
		if opt.Description != "" {
			line += "  " + Dim.Render(opt.Description)
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}
