package components

import (
	"charm.land/bubbles/v2/spinner"
	"charm.land/lipgloss/v2"
)

type Spinner struct {
	frames []string
	style  lipgloss.Style
}

var (
	Dots   = NewSpinner(spinner.Dot.Frames, lipgloss.NewStyle().Foreground(lipgloss.Color("5")))
	Points = NewSpinner(spinner.Points.Frames, lipgloss.NewStyle().Foreground(lipgloss.Color("12")))
)

func NewSpinner(frames []string, style lipgloss.Style) Spinner {
	return Spinner{frames: frames, style: style}
}

func (s Spinner) Frame(tick int) string {
	if len(s.frames) == 0 {
		return ""
	}
	return s.style.Render(s.frames[tick%len(s.frames)])
}

func (s Spinner) Line(tick int, label string) string {
	return s.Frame(tick) + " " + s.style.Render(label)
}
