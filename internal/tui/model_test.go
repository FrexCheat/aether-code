package tui

import (
	"strings"
	"testing"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/conversation"
)

// turnInFlight returns a model with a live turn, the state the input gate
// guards.
func turnInFlight(t *testing.T, cancelled *bool) Model {
	t.Helper()
	m := newModel(Options{Conv: conversation.NewManager()})
	m.live.Begin(make(chan agent.Event), func() { *cancelled = true })
	return m
}

// Quitting must beat the input gate: a /exit typed while the turn is still
// streaming has to leave immediately, cancelling the turn, exactly as ctrl+c
// does — waiting for the turn to end reads as a dropped key.
func TestQuitDuringTurn(t *testing.T) {
	cancelled := false
	m := turnInFlight(t, &cancelled)
	m.input.SetValue("/exit")
	if _, cmd := m.handleEnter(); cmd == nil {
		t.Fatal("no command returned; the quit never reached the program")
	}
	if !cancelled {
		t.Error("the in-flight turn must be cancelled on the way out")
	}
}

// Anything else typed during a turn stays in the box and says so, rather than
// vanishing into the gate.
func TestMessageDuringTurnKeepsTextAndNotices(t *testing.T) {
	cancelled := false
	m := turnInFlight(t, &cancelled)
	m.input.SetValue("hello")
	before := len(m.blocks)
	m, cmd := m.handleEnter()
	if cmd != nil || cancelled {
		t.Errorf("cmd = %v cancelled = %v; a gated message must not start anything", cmd, cancelled)
	}
	if m.input.Value() != "hello" {
		t.Errorf("input = %q, want the text kept in the box", m.input.Value())
	}
	if len(m.blocks) != before+1 || !strings.Contains(m.blocks[before].body, "Still working") {
		t.Errorf("blocks grew by %d, want one notice", len(m.blocks)-before)
	}
}

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
