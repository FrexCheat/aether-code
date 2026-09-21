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
	cmdCompact
	cmdUnknown
)

const availableCommands = "/quit, /exit, /clear, /compact"

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
	case "/compact":
		return cmdCompact, ""
	}
	return cmdUnknown, fmt.Sprintf("Unknown command %q. Available: %s", s, availableCommands)
}
