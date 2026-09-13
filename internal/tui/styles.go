package tui

import (
	"charm.land/bubbles/v2/textarea"
	"charm.land/lipgloss/v2"
)

var (
	styleUserPrefix      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	styleAssistantPrefix = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	styleDim             = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleError           = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	styleWarn            = lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // guard-rail notices (turn limit, unknown tool)
	styleInputBox        = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("8")).
				Padding(0, 1)
	styleStatusBar    = lipgloss.NewStyle().Reverse(true)
	styleNoticePrefix = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	styleBanner       = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	styleTitle        = lipgloss.NewStyle().Bold(true)
	styleReady        = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleSpinner      = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))

	// Tool entries: settled status colors and edit diff line colors.
	styleToolDone   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleToolFailed = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	styleDiffAdd    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleDiffRemove = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

// inputStyles returns textarea styles with no background fill. The bubbles
// default dark theme paints the focused cursor line and the end-of-buffer
// padding black, which reads as a solid black box behind the typed content;
// the rounded frame in render.go already supplies the box, so the content is
// left transparent and muted with the same dim gray used elsewhere in the UI.
// Selection is the one deliberate exception — text under an active selection
// has to stay legible.
func inputStyles() textarea.Styles {
	clean := lipgloss.NewStyle()
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	selection := lipgloss.NewStyle().Background(lipgloss.Color("8"))
	return textarea.Styles{
		Focused: textarea.StyleState{
			Base:        clean,
			Text:        clean,
			CursorLine:  clean,
			EndOfBuffer: dim,
			Placeholder: dim,
			Prompt:      clean,
			Selection:   selection,
		},
		Blurred: textarea.StyleState{
			Base:        clean,
			Text:        dim,
			CursorLine:  clean,
			EndOfBuffer: dim,
			Placeholder: dim,
			Prompt:      clean,
			Selection:   selection,
		},
		Cursor: textarea.CursorStyle{
			Color: lipgloss.Color("7"),
			Blink: true,
		},
	}
}
