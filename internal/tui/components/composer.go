package components

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// FooterData is what the input box's bottom line shows; it is passed in so the
// box stays independent of the caller's state. Usage is the context readout,
// empty when the session has no budget to report.
type FooterData struct {
	Mode     string
	Usage    string
	Scrolled bool
}

// CompletionOption is one candidate in the completion menu: the command name
// without its slash, its argument hint and the description shown beside them.
type CompletionOption struct {
	Name        string
	Hint        string
	Description string
}

// Composer is the input region's fallback owner: the text box, the completion
// menu below it and the footer. The box and the menu are one interaction
// component — the menu never hides the box, unlike a modal owner.
type Composer struct {
	area    textarea.Model
	width   int
	options []CompletionOption
	cursor  int
}

func NewComposer() Composer {
	area := textarea.New()
	area.Prompt = ""
	area.Placeholder = "Send a message..."
	area.ShowLineNumbers = false
	area.CharLimit = 0
	area.MinHeight = 1
	area.MaxHeight = 8
	area.MaxWidth = 0
	area.DynamicHeight = true
	// Enter sends, so the textarea only breaks the line on shift+enter — plus
	// ctrl+j, which terminals that do not speak the kitty keyboard protocol can
	// still tell apart from enter.
	area.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "ctrl+j"))
	area.SetStyles(inputStyles())
	area.Focus()
	return Composer{area: area}
}

// Modal is false: the composer leaves the output region and the live tail alone.
func (c Composer) Modal() bool { return false }

func (c *Composer) SetWidth(w int) {
	c.width = w
	c.area.SetWidth(max(w-6, 10))
}

// SetMaxHeight caps how tall the box grows with its content.
func (c *Composer) SetMaxHeight(h int) { c.area.MaxHeight = max(h, 1) }

func (c Composer) Value() string { return c.area.Value() }

func (c *Composer) SetValue(v string) { c.area.SetValue(v) }

func (c *Composer) Reset() {
	c.area.Reset()
	c.CloseMenu()
}

func (c Composer) Focus() tea.Cmd { return c.area.Focus() }

func (c Composer) Update(msg tea.Msg) (Composer, tea.Cmd) {
	var cmd tea.Cmd
	c.area, cmd = c.area.Update(msg)
	return c, cmd
}

func (c *Composer) OpenMenu(options []CompletionOption) {
	c.options = options
	c.cursor = 0
}

func (c *Composer) CloseMenu() {
	c.options = nil
	c.cursor = 0
}

func (c Composer) MenuActive() bool { return len(c.options) > 0 }

func (c *Composer) MoveMenu(delta int) {
	if len(c.options) == 0 {
		return
	}
	c.cursor = (c.cursor + delta + len(c.options)) % len(c.options)
}

func (c Composer) SelectedMenu() (CompletionOption, bool) {
	if len(c.options) == 0 {
		return CompletionOption{}, false
	}
	return c.options[c.cursor], true
}

// View renders the box, then the completion menu when it is open, then the
// footer.
func (c Composer) View(width int, footer FooterData) string {
	parts := []string{c.boxView()}
	if c.MenuActive() {
		parts = append(parts, c.menuView(width))
	}
	parts = append(parts, c.footerLine(footer))
	return strings.Join(parts, "\n")
}

// boxView renders the bordered text box alone; View places the completion menu
// between it and the footer.
func (c Composer) boxView() string {
	return styleInputBox.Width(max(c.width-2, 10)).Render(c.area.View())
}

// footerLine is the mode/context readout: mode on the left, context occupancy on
// the right, with no border or background.
func (c Composer) footerLine(f FooterData) string {
	left := f.Mode
	if f.Scrolled {
		left += " · scrolled"
	}
	pad := max(1, c.width-lipgloss.Width(left)-lipgloss.Width(f.Usage))
	return Dim.Render(left + strings.Repeat(" ", pad) + f.Usage)
}

// menuView renders one line per candidate, the cursor row marked and the names
// in a shared column.
func (c Composer) menuView(width int) string {
	labels := make([]string, len(c.options))
	nameWidth := 0
	for i, opt := range c.options {
		labels[i] = "/" + opt.Name
		if opt.Hint != "" {
			labels[i] += " " + opt.Hint
		}
		if n := lipgloss.Width(labels[i]); n > nameWidth {
			nameWidth = n
		}
	}
	lines := make([]string, len(c.options))
	for i, opt := range c.options {
		cursor, label := "  ", styleMenuName.Render(labels[i])
		if i == c.cursor {
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

func inputStyles() textarea.Styles {
	clean := lipgloss.NewStyle()
	selection := lipgloss.NewStyle().Background(lipgloss.Color("8"))
	return textarea.Styles{
		Focused: textarea.StyleState{
			Base:        clean,
			Text:        clean,
			CursorLine:  clean,
			EndOfBuffer: Dim,
			Placeholder: Dim,
			Prompt:      clean,
			Selection:   selection,
		},
		Blurred: textarea.StyleState{
			Base:        clean,
			Text:        Dim,
			CursorLine:  clean,
			EndOfBuffer: Dim,
			Placeholder: Dim,
			Prompt:      clean,
			Selection:   selection,
		},
		Cursor: textarea.CursorStyle{
			Color: lipgloss.Color("7"),
			Blink: true,
		},
	}
}
