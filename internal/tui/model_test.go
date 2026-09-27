package tui

import (
	"strings"
	"testing"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/command"
	"github.com/FrexCheat/aether-code/internal/conversation"
)

// turnInFlight returns a model with a live turn, the state the input gate
// guards.
func turnInFlight(t *testing.T, cancelled *bool) Model {
	t.Helper()
	m := newModel(Options{
		Conv:     conversation.NewManager(),
		Commands: command.New(command.Builtins()...),
	})
	m.live.Begin(make(chan agent.Event), func() { *cancelled = true })
	return m
}

// Quitting must beat the input gate: a /exit typed while the turn is still
// streaming has to leave immediately, cancelling the turn, exactly as ctrl+c
// does — waiting for the turn to end reads as a dropped key.
func TestQuitDuringTurn(t *testing.T) {
	cancelled := false
	m := turnInFlight(t, &cancelled)
	m.composer.SetValue("/exit")
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
	m.composer.SetValue("hello")
	before := len(m.blocks)
	m, cmd := m.handleEnter()
	if cmd != nil || cancelled {
		t.Errorf("cmd = %v cancelled = %v; a gated message must not start anything", cmd, cancelled)
	}
	if m.composer.Value() != "hello" {
		t.Errorf("input = %q, want the text kept in the box", m.composer.Value())
	}
	if len(m.blocks) != before+1 || !strings.Contains(m.blocks[before].body, "Still working") {
		t.Errorf("blocks grew by %d, want one notice", len(m.blocks)-before)
	}
}
