package command

import (
	"fmt"
	"strings"
)

// Builtins returns the commands that ship with the app, in the order /help
// lists them.
func Builtins() []*Command {
	return []*Command{
		exitCommand(),
		helpCommand(),
		compactCommand(),
		clearCommand(),
		sessionCommand(),
		statusCommand(),
		reviewCommand(),
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

func reviewCommand() *Command {
	return &Command{
		Name:        "review",
		Description: "Review uncommitted changes with the model",
		Usage:       "/review [path or commit]",
		ArgHint:     "[path or commit]",
		Kind:        Prompt,
		Prompt:      reviewPrompt,
	}
}

// reviewPrompt is model-facing text, so it is Chinese like the other
// model-facing prompts in the project.
const reviewPrompt = `请审查当前工作区的未提交改动。
先运行 git status 和 git diff 了解改动范围，再按严重程度列出问题，每条给出文件与行号、问题说明与修改建议；没有发现问题时明确说明。
重点关注：正确性、边界条件、并发安全、错误处理、可读性与命名，以及是否与既有代码风格一致。

审查范围（为空则审查全部未提交改动）：$ARGUMENTS`

// helpText is one compact line per command: "/name [arg] — description".
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

func statusText(s Status, t TokenUsage) string {
	provider := s.Provider
	if s.Protocol != "" {
		provider = fmt.Sprintf("%s (%s)", s.Provider, s.Protocol)
	}
	session := s.SessionID
	if session != "" {
		session = fmt.Sprintf("%s · %d messages", session, s.Messages)
	}
	context := fmt.Sprintf("%s / %s · %s", formatTokens(t.Used), formatTokens(t.Window), t.Level)

	rows := [][2]string{
		{"version", s.Version},
		{"model", s.Model},
		{"provider", provider},
		{"workdir", s.WorkDir},
		{"session", session},
		{"context", context},
		{"mode", s.Mode},
		{"mcp", s.MCP},
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

func formatTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000), ".0") + "k"
}
