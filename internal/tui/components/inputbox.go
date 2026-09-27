package components

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var styleInputBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)

// FooterData is what the input box's bottom line shows; it is passed in so the
// box stays independent of the caller's state. Usage is the context readout,
// empty when the session has no budget to report.
type FooterData struct {
	Mode     string
	Usage    string
	Scrolled bool
}

type InputBox struct {
	area  textarea.Model
	width int
}

func NewInputBox() InputBox {
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
	return InputBox{area: area}
}

func (b *InputBox) SetWidth(w int) {
	b.width = w
	b.area.SetWidth(max(w-6, 10))
}

// SetMaxHeight caps how tall the box grows with its content.
func (b *InputBox) SetMaxHeight(h int) { b.area.MaxHeight = max(h, 1) }

func (b InputBox) Update(msg tea.Msg) (InputBox, tea.Cmd) {
	var cmd tea.Cmd
	b.area, cmd = b.area.Update(msg)
	return b, cmd
}

// boxView renders the bordered text box alone; the composer places the
// completion menu between it and the footer.
func (b InputBox) boxView() string {
	return styleInputBox.Width(max(b.width-2, 10)).Render(b.area.View())
}

func (b InputBox) Value() string { return b.area.Value() }

func (b *InputBox) SetValue(v string) { b.area.SetValue(v) }

func (b *InputBox) Reset() { b.area.Reset() }

func (b InputBox) Focus() tea.Cmd { return b.area.Focus() }

// footerLine is the mode/context readout: mode on the left, context occupancy on
// the right, with no border or background.
func (b InputBox) footerLine(f FooterData) string {
	left := f.Mode
	if f.Scrolled {
		left += " · scrolled"
	}
	pad := max(1, b.width-lipgloss.Width(left)-lipgloss.Width(f.Usage))
	return Dim.Render(left + strings.Repeat(" ", pad) + f.Usage)
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
