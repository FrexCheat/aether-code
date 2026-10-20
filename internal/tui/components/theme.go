package components

import "charm.land/lipgloss/v2"

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

var (
	styleInputBox     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
	styleMenuCursor   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	styleMenuName     = lipgloss.NewStyle().Bold(true)
	stylePermBox      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("3")).Padding(0, 1)
	stylePermTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	stylePermTool     = lipgloss.NewStyle().Bold(true)
	stylePermCursor   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	stylePermSelected = lipgloss.NewStyle().Bold(true)
)

const ToolIndent = "  "
