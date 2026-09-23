package components

import "charm.land/lipgloss/v2"

// The one palette. The widgets in this package and the tui's block renderer draw
// from it, so a colour is defined once.
var (
	Dim          = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	ErrorText    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	WarnText     = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	Notice       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	Logo         = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	Title        = lipgloss.NewStyle().Bold(true)
	ToolDone     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	DiffAdd      = lipgloss.NewStyle().Background(lipgloss.Color("2"))
	DiffRemove   = lipgloss.NewStyle().Background(lipgloss.Color("1"))
	UserBubble   = lipgloss.NewStyle().Background(lipgloss.Color("8")).Padding(0, 1)
	AssistantDot = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
)

// ToolIndent is the two-space prefix every tool line carries.
const ToolIndent = "  "
