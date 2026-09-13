package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/tools"
)

// freeze the clock so lifecycle timing (turn timer) is deterministic.
func init() {
	timeNow = func() time.Time { return time.Unix(1000, 0) }
	timeSince = func(time.Time) time.Duration { return 7 * time.Second }
}

func TestWholeTurnCommitOnStreamEnd(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{
		llm.TextDelta{Text: "a"}, llm.TextDelta{Text: "b"}, llm.StreamEnd{},
	}}
	m, rec := newTestModel(fake)
	m = send(t, m, "q")
	if m.conv.Len() != 2 {
		t.Fatalf("conv.Len = %d, want 2", m.conv.Len())
	}
	if m.live != nil {
		t.Fatal("stream should be idle after Done")
	}
	// No TextComplete in the script: the reply flushes at loop end, above the
	// footer (same chunk or its own, order holds either way).
	chat := ansi.Strip(rec.joined())
	if !strings.Contains(chat, "ab") {
		t.Errorf("committed reply should print:\n%s", chat)
	}
	if last := ansi.Strip(rec.lastChunk()); !strings.Contains(last, "7s · done") {
		t.Errorf("last chunk = %q, want turn footer", last)
	}
}

func TestErrorDoesNotCommitAssistantReply(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{llm.StreamError{Err: errors.New("boom")}}}
	m, rec := newTestModel(fake)
	m = send(t, m, "q")
	// The user question is committed by the agent before streaming; the
	// failed turn's assistant reply is not.
	if m.conv.Len() != 1 {
		t.Fatalf("conv.Len = %d, want 1 (user question only)", m.conv.Len())
	}
	if last := ansi.Strip(rec.lastChunk()); !strings.Contains(last, "Error: boom") {
		t.Errorf("last chunk = %q, want error notice", last)
	}
}

func TestThinkingIgnored(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{
		llm.TextDelta{Text: "visible"},
		llm.ThinkingDelta{Text: "hidden-thinking"},
		llm.TextDelta{Text: "!"},
		llm.StreamEnd{},
	}}
	m, rec := newTestModel(fake)
	m = send(t, m, "q")
	chat := ansi.Strip(rec.joined())
	if !strings.Contains(chat, "visible!") {
		t.Fatalf("assistant text = want 'visible!', got:\n%s", chat)
	}
	if strings.Contains(chat, "hidden-thinking") {
		t.Error("thinking must not print")
	}
}

// fakeEditTool is a registered edit_file stand-in whose result carries a diff.
type fakeEditTool struct{}

func (fakeEditTool) Name() string             { return "edit_file" }
func (fakeEditTool) Description() string      { return "fake" }
func (fakeEditTool) Category() tools.Category { return tools.CategoryWrite }
func (fakeEditTool) Schema() tools.ToolSchema { return tools.ToolSchema{Name: "edit_file"} }
func (fakeEditTool) Title(args map[string]any) string {
	if p, _ := args["file_path"].(string); p != "" {
		return "Edit " + filepath.Base(p)
	}
	return ""
}
func (fakeEditTool) Execute(context.Context, map[string]any) tools.ToolResult {
	return tools.ToolResult{Output: "-      2  beta\n+      2  BETA", Diff: true}
}

func TestToolEntryLifecycleAndDiff(t *testing.T) {
	conv := conversation.NewManager()
	reg := tools.NewRegistry()
	reg.Register(fakeEditTool{})
	ag := agent.New(&fakeProvider{scripts: [][]llm.StreamEvent{
		{
			llm.ToolCallComplete{ToolID: "tu_1", ToolName: "edit_file",
				Arguments: map[string]any{"file_path": "main.go"}},
			llm.StreamEnd{},
		},
		{llm.StreamEnd{}},
	}}, reg, conv, "sys", false, config.DefaultMaxIterations)
	m := newModel(Options{Agent: ag, Conv: conv, Registry: reg, Version: "test", WorkDir: "/tmp"})
	rec := &printRecorder{}
	m.print = rec.record
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = mm.(Model)
	m = send(t, m, "edit it")

	// A tool-only turn produces no assistant block — empty text segments are
	// skipped instead of rendering a husk.
	tool := rec.lastChunk()
	stripped := ansi.Strip(tool)
	if !strings.Contains(stripped, "● Edit main.go") {
		t.Fatalf("tool block missing title:\n%q", tool)
	}
	if !strings.Contains(stripped, "(0.0s)") {
		t.Errorf("tool block missing elapsed time:\n%s", stripped)
	}
	if !strings.Contains(stripped, "beta") || !strings.Contains(stripped, "BETA") {
		t.Errorf("diff content missing:\n%s", stripped)
	}
	// Diff lines indent under the title text, after the dot.
	if !strings.Contains(stripped, "    +      2  BETA") {
		t.Errorf("diff line not indented under the tool title:\n%s", stripped)
	}
	// Colored: the printed chunk must carry the green/red styles.
	if !strings.Contains(tool, styleDiffAdd.Render("+      2  BETA")) {
		t.Errorf("diff add line not colored:\n%q", tool)
	}
	// History: user → assistant(tool_use) → user(tool_result).
	if conv.Len() != 3 {
		t.Fatalf("conv.Len = %d, want 3", conv.Len())
	}
	if len(conv.GetMessages()[1].ToolUses) != 1 {
		t.Errorf("assistant tool uses = %+v", conv.GetMessages()[1].ToolUses)
	}
}

func TestUnknownToolRendersFailed(t *testing.T) {
	conv := conversation.NewManager()
	ag := agent.New(&fakeProvider{scripts: [][]llm.StreamEvent{
		{
			llm.ToolCallComplete{ToolID: "tu_x", ToolName: "no_such_tool", Arguments: map[string]any{}},
			llm.StreamEnd{},
		},
		{llm.StreamEnd{}},
	}}, tools.NewRegistry(), conv, "sys", false, config.DefaultMaxIterations)
	m := newModel(Options{Agent: ag, Conv: conv, Version: "test", WorkDir: "/tmp"})
	rec := &printRecorder{}
	m.print = rec.record
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = mm.(Model)
	m = send(t, m, "do it")

	raw := rec.lastChunk()
	if !strings.Contains(raw, styleToolFailed.Render("●")) {
		t.Errorf("failed tool should render a red dot:\n%q", raw)
	}
	if !strings.Contains(ansi.Strip(raw), "no_such_tool") {
		t.Errorf("failed tool title missing:\n%q", raw)
	}
}

// TestRunningToolRendersSpinner checks the in-flight tool line: the spinner
// frame leads the title, indented one step right of user/assistant messages.
func TestRunningToolRendersSpinner(t *testing.T) {
	m, _ := startBlockedLive(t)
	mm, _ := m.Update(streamBatchMsg{
		agent.ToolCallBegin{ToolID: "tu_1", ToolName: "read_file", Args: map[string]any{"file_path": "a.go"}},
	})
	m = mm.(Model)
	frame := ansi.Strip(m.View().Content)
	// The spinner has not ticked, so the model's current frame is deterministic.
	want := toolIndent + m.spinner.View() + " Read a.go"
	if !strings.Contains(frame, want) {
		t.Errorf("running tool line = want %q, got:\n%s", want, frame)
	}
	m.live.cancel()
}

func TestTextFlushesBeforeToolEntry(t *testing.T) {
	fake := &fakeProvider{scripts: [][]llm.StreamEvent{
		{
			llm.TextDelta{Text: "先看一下文件"},
			llm.TextComplete{},
			llm.ToolCallComplete{ToolID: "tu_1", ToolName: "read_file",
				Arguments: map[string]any{"file_path": "a.go"}},
			llm.StreamEnd{},
		},
		{llm.StreamEnd{}},
	}}
	m, rec := newTestModel(fake)
	m = send(t, m, "q")

	// The text block ends (TextComplete) before the tool call: the assistant
	// block must print above the tool block, not be deferred to turn end.
	chat := ansi.Strip(rec.joined())
	ia, ib := strings.Index(chat, "先看一下文件"), strings.Index(chat, "Read a.go")
	if ia < 0 || ib < 0 {
		t.Fatalf("missing blocks:\n%s", chat)
	}
	if ia > ib {
		t.Errorf("assistant text should print above the tool block:\n%s", chat)
	}
}

func TestToolCallBeginFlushesWithoutTextComplete(t *testing.T) {
	// Without TextComplete the text stays buffered until turn end; the tool
	// block still prints (display order may invert, history stays correct).
	fake := &fakeProvider{scripts: [][]llm.StreamEvent{
		{
			llm.TextDelta{Text: "fallback"},
			llm.ToolCallComplete{ToolID: "tu_1", ToolName: "read_file",
				Arguments: map[string]any{"file_path": "a.go"}},
			llm.StreamEnd{},
		},
		{llm.StreamEnd{}},
	}}
	m, rec := newTestModel(fake)
	m = send(t, m, "q")
	chat := ansi.Strip(rec.joined())
	ia, ib := strings.Index(chat, "Read a.go"), strings.Index(chat, "fallback")
	if ia < 0 || ib < 0 {
		t.Fatalf("missing blocks:\n%s", chat)
	}
	if ia > ib {
		t.Errorf("tool block should print before the deferred assistant text:\n%s", chat)
	}
}
