package tui

import "charm.land/lipgloss/v2"

var (
	styleUserPrefix      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	styleAssistantPrefix = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	styleDim             = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleError           = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	styleInputBox        = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("8")).
				Padding(0, 1)
	styleStatusBar = lipgloss.NewStyle().Reverse(true)
	styleBanner    = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	styleTitle     = lipgloss.NewStyle().Bold(true)
	styleReady     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)
