package tui

import (
	"strings"
	"testing"
)

func TestCommandTableDerivation(t *testing.T) {
	names := commandNames()
	if len(names) == 0 {
		t.Fatal("the command table is empty")
	}
	for _, name := range names {
		cmd, hint := parseCommand(name)
		if cmd == cmdUnknown || hint != "" {
			t.Errorf("parseCommand(%q) = %v/%q, want a known command", name, cmd, hint)
		}
	}
	// Aliases parse too.
	if cmd, _ := parseCommand("/exit"); cmd != cmdQuit {
		t.Errorf("alias /exit = %v, want cmdQuit", cmd)
	}
	// The unknown-command hint and the banner tips derive from the table.
	_, hint := parseCommand("/nope")
	for _, name := range names {
		if !strings.Contains(hint, name) {
			t.Errorf("hint %q misses %q", hint, name)
		}
	}
	for _, name := range names {
		if !strings.Contains(tipsText, name) {
			t.Errorf("banner tips %q miss %q", tipsText, name)
		}
	}
}
