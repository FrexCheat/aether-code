package tui

import (
	"strings"
	"testing"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/tui/components"
)

// A modal input owner takes the input region and suspends the output region's
// live tail, leaving completed history alone.
func TestModalInputSuspendsLiveTail(t *testing.T) {
	m := dispatchModel(t)
	m.width = 80
	m.history = []string{"history-line"}
	m.live.Begin(make(chan agent.Event), func() {})
	m.dialog.Open(&permissions.Request{
		ToolName: "bash",
		Reply:    make(chan permissions.Response, 1),
	})

	if out := m.outputLines(); len(out) != 1 || out[0] != "history-line" {
		t.Errorf("output = %q, want only the completed history", out)
	}
	in := strings.Join(m.inputLines(), "\n")
	if !strings.Contains(in, "Permission required") {
		t.Errorf("input region must show the dialog:\n%s", in)
	}
	if strings.Contains(in, "Send a message") {
		t.Error("the composer must be hidden while the dialog is up")
	}
}

// A non-modal owner (the composer with its completion menu) leaves the output
// region untouched and keeps the input box visible.
func TestCompletionMenuLeavesOutputAlone(t *testing.T) {
	m := dispatchModel(t)
	m.history = []string{"history-line"}
	m.live.Begin(make(chan agent.Event), func() {})
	m.composer.OpenMenu([]components.CompletionOption{{Name: "exit"}, {Name: "help"}})

	out := strings.Join(m.outputLines(), "\n")
	if !strings.Contains(out, "history-line") || !strings.Contains(out, "Imagining") {
		t.Errorf("a non-modal menu must not suspend the live tail:\n%s", out)
	}
	in := strings.Join(m.inputLines(), "\n")
	if !strings.Contains(in, "/exit") || !strings.Contains(in, "/help") {
		t.Errorf("the menu must render in the input region:\n%s", in)
	}
}
