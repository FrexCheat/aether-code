package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/FrexCheat/aether-code/internal/command"
	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
)

func keyMsg(name string) tea.KeyPressMsg {
	switch name {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	return tea.KeyPressMsg{}
}

// dispatchModel is a model with the built-in command registry and enough
// options for /help and /status to have something to say.
func dispatchModel(t *testing.T) Model {
	t.Helper()
	return newModel(Options{
		Conv:     conversation.NewManager(),
		Commands: command.New(command.Builtins()...),
		Version:  "9.9.9",
		WorkDir:  t.TempDir(),
		Provider: config.Provider{Name: "prov", Model: "mod", Protocol: config.ProtocolAnthropic},
	})
}

func TestDispatchHelpListsCommands(t *testing.T) {
	m := dispatchModel(t)
	m.composer.SetValue("/help")
	before := len(m.transcript.blocks)
	m, _ = m.handleEnter()
	if len(m.transcript.blocks) != before+1 {
		t.Fatalf("blocks grew by %d, want 1", len(m.transcript.blocks)-before)
	}
	body := m.transcript.blocks[before].body
	for _, want := range []string{"/exit", "/skills [reload]", "/session [list]"} {
		if !strings.Contains(body, want) {
			t.Errorf("help misses %q:\n%s", want, body)
		}
	}
	if m.composer.Value() != "" {
		t.Errorf("composer not reset: %q", m.composer.Value())
	}
}

func TestDispatchUnknownPointsToHelp(t *testing.T) {
	m := dispatchModel(t)
	for _, input := range []string{"/nope", "/"} {
		m.composer.SetValue(input)
		before := len(m.transcript.blocks)
		m, _ = m.handleEnter()
		if body := m.transcript.blocks[before].body; !strings.Contains(body, "/help") {
			t.Errorf("%q hint misses /help: %q", input, body)
		}
	}
}

func TestDispatchStatusShowsSnapshot(t *testing.T) {
	m := dispatchModel(t)
	m.composer.SetValue("/status")
	before := len(m.transcript.blocks)
	m, _ = m.handleEnter()
	body := m.transcript.blocks[before].body
	for _, want := range []string{"version", "9.9.9", "mod", "workdir"} {
		if !strings.Contains(body, want) {
			t.Errorf("status misses %q:\n%s", want, body)
		}
	}
}

func TestDispatchCompactWithoutCompactor(t *testing.T) {
	m := dispatchModel(t)
	m.composer.SetValue("/compact")
	before := len(m.transcript.blocks)
	m, cmd := m.handleEnter()
	if cmd != nil {
		t.Errorf("compaction without a compactor must not start a command")
	}
	if !strings.Contains(m.transcript.blocks[before].body, "not available") {
		t.Errorf("compact notice = %q", m.transcript.blocks[before].body)
	}
}

// The input gate blocks every command but /exit while a turn streams.
func TestDispatchGateBlocksHelpAllowsExit(t *testing.T) {
	cancelled := false
	m := dispatchModel(t)
	m.turn = &turnStream{cancel: func() { cancelled = true }, stop: make(chan struct{})}
	m.live.Begin()

	m.composer.SetValue("/help")
	before := len(m.transcript.blocks)
	m, cmd := m.handleEnter()
	if cmd != nil || cancelled {
		t.Errorf("help must be gated, cmd=%v cancelled=%v", cmd, cancelled)
	}
	if !strings.Contains(m.transcript.blocks[before].body, "Still working") {
		t.Errorf("gate notice = %q", m.transcript.blocks[before].body)
	}

	m.composer.SetValue("/exit")
	if _, cmd := m.handleEnter(); cmd == nil {
		t.Error("/exit must run mid-turn")
	}
	if !cancelled {
		t.Error("/exit must cancel the turn")
	}
}

func TestTabCompletion(t *testing.T) {
	m := dispatchModel(t)

	// One candidate fills the name and adds a trailing space.
	m.composer.SetValue("/cl")
	m, _ = m.handleTab()
	if m.composer.Value() != "/clear " {
		t.Errorf("single match = %q, want %q", m.composer.Value(), "/clear ")
	}
	if m.composer.MenuActive() {
		t.Error("a single match must not open the menu")
	}

	// Several candidates open the menu; arrows move and enter accepts.
	m.composer.SetValue("/")
	m, _ = m.handleTab()
	if !m.composer.MenuActive() {
		t.Fatal("a bare slash must open the menu")
	}
	m, _ = m.handleCompletionKey(keyMsg("down"))
	if option, _ := m.composer.SelectedMenu(); option.Name != "help" {
		t.Errorf("after one down the selection = %q, want help", option.Name)
	}
	m, _ = m.handleCompletionKey(keyMsg("enter"))
	if m.composer.Value() != "/help " {
		t.Errorf("accepted value = %q, want %q", m.composer.Value(), "/help ")
	}
	if m.composer.MenuActive() {
		t.Error("the menu must close after accepting")
	}

	// Esc closes without a fill.
	m.composer.SetValue("/")
	m, _ = m.handleTab()
	m, _ = m.handleCompletionKey(keyMsg("esc"))
	if m.composer.MenuActive() {
		t.Error("esc must close the menu")
	}
}
