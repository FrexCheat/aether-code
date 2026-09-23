package components

import (
	"charm.land/bubbles/v2/spinner"
	"charm.land/lipgloss/v2"
)

// Spinner is one indicator's frames and the style they render in. It carries no
// animation state — Frame is a pure function of the clock's index — so any row
// can show any shape and picking a different shape is picking a different
// Spinner.
type Spinner struct {
	frames []string
	style  lipgloss.Style
}

// The shapes the UI picks from.
var (
	Dots   = NewSpinner(spinner.Dot.Frames, lipgloss.NewStyle().Foreground(lipgloss.Color("5")))
	Points = NewSpinner(spinner.Points.Frames, lipgloss.NewStyle().Foreground(lipgloss.Color("12")))
)

func NewSpinner(frames []string, style lipgloss.Style) Spinner {
	return Spinner{frames: frames, style: style}
}

// Frame is the glyph at a clock index.
func (s Spinner) Frame(tick int) string {
	if len(s.frames) == 0 {
		return ""
	}
	return s.style.Render(s.frames[tick%len(s.frames)])
}

// Line is the glyph followed by a label, both in the spinner's style.
func (s Spinner) Line(tick int, label string) string {
	return s.Frame(tick) + " " + s.style.Render(label)
}
