package command

import (
	"fmt"
	"strings"

	"github.com/FrexCheat/aether-code/internal/format"
)

func Builtins() []*Command {
	return []*Command{
		exitCommand(),
		helpCommand(),
		compactCommand(),
		clearCommand(),
		sessionCommand(),
		statusCommand(),
		skillsCommand(),
	}
}

func exitCommand() *Command {
	return &Command{
		Name:        "exit",
		Description: "Leave Aether Code",
		Usage:       "/exit",
		Kind:        Local,
		Immediate:   true,
		Run: func(h Host, _ []string) error {
			h.Quit()
			return nil
		},
	}
}

func helpCommand() *Command {
	return &Command{
		Name:        "help",
		Description: "List the available commands",
		Usage:       "/help",
		Kind:        Local,
		Run: func(h Host, _ []string) error {
			h.Message(helpText(h.Commands()), Info)
			return nil
		},
	}
}

func compactCommand() *Command {
	return &Command{
		Name:        "compact",
		Description: "Summarize the conversation to free context",
		Usage:       "/compact",
		Kind:        UI,
		Run: func(h Host, _ []string) error {
			h.Compact()
			return nil
		},
	}
}

func clearCommand() *Command {
	return &Command{
		Name:        "clear",
		Description: "Start a fresh session",
		Usage:       "/clear",
		Kind:        UI,
		Run: func(h Host, _ []string) error {
			return h.NewSession()
		},
	}
}

func sessionCommand() *Command {
	return &Command{
		Name:        "session",
		Description: "Show the current session, or list saved ones",
		Usage:       "/session [list]",
		ArgHint:     "[list]",
		Kind:        Local,
		Run: func(h Host, args []string) error {
			switch {
			case len(args) == 0:
				h.Message(currentSessionText(h.CurrentSession()), Info)
				return nil
			case strings.EqualFold(args[0], "list"):
				sessions, err := h.Sessions()
				if err != nil {
					return err
				}
				h.Message(sessionListText(sessions), Info)
				return nil
			default:
				return fmt.Errorf("usage: %s", "/session [list]")
			}
		},
	}
}

func statusCommand() *Command {
	return &Command{
		Name:        "status",
		Description: "Show version, model, session and context status",
		Usage:       "/status",
		Kind:        Local,
		Run: func(h Host, _ []string) error {
			h.Message(statusText(h.Status(), h.TokenUsage()), Info)
			return nil
		},
	}
}

func skillsCommand() *Command {
	return &Command{
		Name:        "skills",
		Description: "List the loaded Skills, or rescan the directories",
		Usage:       "/skills [reload]",
		ArgHint:     "[reload]",
		Kind:        Local,
		Run: func(h Host, args []string) error {
			switch {
			case len(args) == 0:
				h.Message(skillListText(h.Skills()), Info)
				return nil
			case strings.EqualFold(args[0], "reload"):
				h.ReloadSkills()
				return nil
			default:
				return fmt.Errorf("usage: %s", "/skills [reload]")
			}
		},
	}
}

func helpText(commands []*Command) string {
	lines := make([]string, 0, len(commands))
	for _, c := range commands {
		usage := c.Usage
		if len(c.Aliases) > 0 {
			names := make([]string, len(c.Aliases))
			for i, a := range c.Aliases {
				names[i] = "/" + a
			}
			usage += " (aliases: " + strings.Join(names, ", ") + ")"
		}
		lines = append(lines, fmt.Sprintf("%s — %s", usage, c.Description))
	}
	return strings.Join(lines, "\n")
}

func currentSessionText(s SessionInfo) string {
	if s.ID == "" {
		return "No active session."
	}
	return fmt.Sprintf("Session %s\n%d messages", s.ID, s.Messages)
}

func sessionListText(sessions []SessionInfo) string {
	if len(sessions) == 0 {
		return "No saved sessions for this directory."
	}
	lines := make([]string, 0, len(sessions)+1)
	lines = append(lines, fmt.Sprintf("Sessions (%d):", len(sessions)))
	for _, s := range sessions {
		mark := "  "
		if s.Current {
			mark = "* "
		}
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		lines = append(lines, fmt.Sprintf("%s%s  %s  %d msg  %s",
			mark, s.ID, title, s.Messages, s.ModTime.Format("2006-01-02 15:04")))
	}
	return strings.Join(lines, "\n")
}

func skillListText(report SkillReport) string {
	if len(report.Skills) == 0 {
		return "No Skills loaded."
	}
	width := 0
	for _, skill := range report.Skills {
		width = max(width, len(skill.Name))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Skills (%d):\n", len(report.Skills))
	for _, skill := range report.Skills {
		mark := " "
		if skill.Active {
			mark = "*"
		}
		fmt.Fprintf(&b, "%s %-*s  %s  [%s]\n", mark, width, skill.Name, skill.Description, skill.Source)
	}
	for _, skill := range report.Skills {
		if !skill.Slash {
			fmt.Fprintf(&b, "\n/%s is taken by a built-in command; the Skill still loads by name.\n", skill.Name)
		}
	}
	for _, note := range report.Notes {
		b.WriteString("\n" + note + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func statusText(s Status, t TokenUsage) string {
	provider := s.Provider
	if s.Protocol != "" {
		provider = fmt.Sprintf("%s (%s)", s.Provider, s.Protocol)
	}
	session := s.SessionID
	if session != "" {
		session = fmt.Sprintf("%s · %d messages", session, s.Messages)
	}
	context := fmt.Sprintf("%s / %s · %s", format.Tokens(t.Used), format.Tokens(t.Window), t.Level)

	rows := [][2]string{
		{"version", s.Version},
		{"model", s.Model},
		{"provider", provider},
		{"workdir", s.WorkDir},
		{"session", session},
		{"context", context},
		{"mode", s.Mode},
		{"mcp", s.MCP},
		{"skills", s.Skills},
	}
	var b strings.Builder
	for _, row := range rows {
		if row[1] == "" {
			continue
		}
		fmt.Fprintf(&b, "%-9s %s\n", row[0], row[1])
	}
	return strings.TrimRight(b.String(), "\n")
}
