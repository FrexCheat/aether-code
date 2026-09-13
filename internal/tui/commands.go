package tui

import (
	"fmt"
	"strings"
)

type command int

const (
	cmdNone command = iota // not a slash command
	cmdQuit
	cmdClear
	cmdPlan // /plan: switch the agent to read-only plan mode
	cmdDo   // /do: switch back to normal mode to execute the plan
	cmdUnknown
)

const availableCommands = "/quit, /exit, /clear, /plan, /do"

// parseCommand classifies user input. For cmdUnknown the returned string is
// the hint to show; otherwise it is empty.
func parseCommand(input string) (command, string) {
	s := strings.TrimSpace(input)
	if !strings.HasPrefix(s, "/") {
		return cmdNone, ""
	}
	switch s {
	case "/quit", "/exit":
		return cmdQuit, ""
	case "/clear":
		return cmdClear, ""
	case "/plan":
		return cmdPlan, ""
	case "/do":
		return cmdDo, ""
	}
	return cmdUnknown, fmt.Sprintf("Unknown command %q. Available: %s", s, availableCommands)
}
