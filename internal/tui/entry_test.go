package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/tools"
)

// freeze the clock so lifecycle timing (turn timer, tool elapsed) is
// deterministic.
func init() {
	timeNow = func() time.Time { return time.Unix(1000, 0) }
	timeSince = func(time.Time) time.Duration { return 7 * time.Second }
}

func TestCachedRenderMemoizesPerWidth(t *testing.T) {
	e := newAssistantEntry("x")
	calls := 0
	render := func(md string) string { calls++; return "<" + md + ">" }
	if got := e.cachedRender(80, render); got != "<x>" || calls != 1 {
		t.Fatalf("first = %q calls=%d", got, calls)
	}
	if got := e.cachedRender(80, render); got != "<x>" || calls != 1 {
		t.Fatalf("cached = %q calls=%d", got, calls)
	}
	if got := e.cachedRender(40, render); got != "<x>" || calls != 2 {
		t.Fatalf("after width change = %q calls=%d", got, calls)
	}
}

func TestWholeTurnCommitOnStreamEnd(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{
		llm.TextDelta{Text: "a"}, llm.TextDelta{Text: "b"}, llm.StreamEnd{},
	}}
	m := send(t, newTestModel(fake), "q")
	last := lastEntry(t, m)
	if last.kind != entryTurn {
		t.Fatalf("last entry = %+v, want turn footer", last)
	}
	if last.elapsed != 7*time.Second || last.doneAt.IsZero() {
		t.Fatalf("turn footer timing not set: %+v", last)
	}
	reply := m.entries[len(m.entries)-2]
	if reply.kind != entryAssistant || reply.text != "ab" {
		t.Fatalf("reply entry = %+v, want assistant 'ab'", reply)
	}
	if m.conv.Len() != 2 {
		t.Fatalf("conv.Len = %d, want 2", m.conv.Len())
	}
	if m.live != nil {
		t.Fatal("stream should be idle after Done")
	}
	chat := ansi.Strip(renderTop(&m))
	if !strings.Contains(chat, "ab") || !strings.Contains(chat, "7s · done") {
		t.Errorf("committed reply should render with turn footer:\n%s", chat)
	}
}

func TestErrorDoesNotCommitAssistantReply(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{llm.StreamError{Err: errors.New("boom")}}}
	m := send(t, newTestModel(fake), "q")
	// The user question is committed by the agent before streaming; the
	// failed turn's assistant reply is not.
	if m.conv.Len() != 1 {
		t.Fatalf("conv.Len = %d, want 1 (user question only)", m.conv.Len())
	}
	last := lastEntry(t, m)
	if last.kind != entryNotice || last.severity != noticeError {
		t.Fatalf("last entry = %+v, want error notice", last)
	}
	if !strings.Contains(ansi.Strip(renderTop(&m)), "Error: boom") {
		t.Error("error notice should render")
	}
}

func TestThinkingIgnored(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{
		llm.TextDelta{Text: "visible"},
		llm.ThinkingDelta{Text: "hidden-thinking"},
		llm.TextDelta{Text: "!"},
		llm.StreamEnd{},
	}}
	m := send(t, newTestModel(fake), "q")
	reply := m.entries[len(m.entries)-2] // turn footer is last
	if reply.text != "visible!" {
		t.Fatalf("assistant text = %q, thinking must be ignored", reply.text)
	}
	if strings.Contains(ansi.Strip(renderTop(&m)), "hidden-thinking") {
		t.Error("thinking must not render")
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
	m = send(t, m, "edit it")

	// Entries: user, tool (settled), turn footer. A tool-only turn produces
	// no assistant entry — empty text segments are skipped instead of
	// rendering a husk.
	if len(m.entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(m.entries))
	}
	tool := m.entries[1]
	if tool.kind != entryTool || tool.toolName != "edit_file" || tool.state != toolDone {
		t.Fatalf("tool entry = %+v", tool)
	}
	if tool.text != "Edit main.go" {
		t.Errorf("tool title = %q, want %q", tool.text, "Edit main.go")
	}

	chat := ansi.Strip(renderTop(&m))
	if !strings.Contains(chat, "⎿ ✓ Edit main.go") || !strings.Contains(chat, "(0.0s)") {
		t.Errorf("tool status line missing:\n%s", chat)
	}
	if !strings.Contains(chat, "beta") || !strings.Contains(chat, "BETA") {
		t.Errorf("diff content missing:\n%s", chat)
	}
	// Colored: the raw render must carry the green/red styles.
	raw := renderTop(&m)
	if !strings.Contains(raw, styleDiffAdd.Render("+      2  BETA")) {
		t.Errorf("diff add line not colored:\n%s", raw)
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
	m = send(t, m, "do it")

	tool := m.entries[1]
	if tool.kind != entryTool || tool.state != toolFailed {
		t.Fatalf("tool entry = %+v, want failed tool entry", tool)
	}
	chat := ansi.Strip(renderTop(&m))
	if !strings.Contains(chat, "⎿ ✗") {
		t.Errorf("failed tool marker missing:\n%s", chat)
	}
}

// lastEntry returns the most recent committed entry.
func lastEntry(t *testing.T, m Model) *entry {
	t.Helper()
	if len(m.entries) == 0 {
		t.Fatal("no entries")
	}
	return &m.entries[len(m.entries)-1]
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
	m := send(t, newTestModel(fake), "q")

	// The text block ends (TextComplete) before the tool call: the assistant
	// entry must sit above the tool entry, not be deferred to turn end.
	if len(m.entries) != 4 {
		t.Fatalf("entries = %d, want 4 (user, assistant, tool, turn)", len(m.entries))
	}
	if m.entries[1].kind != entryAssistant || m.entries[1].text != "先看一下文件" {
		t.Errorf("entries[1] = %+v, want assistant text before the tool entry", m.entries[1])
	}
	if m.entries[2].kind != entryTool || m.entries[2].toolName != "read_file" {
		t.Errorf("entries[2] = %+v, want tool entry", m.entries[2])
	}
	chat := ansi.Strip(renderTop(&m))
	if strings.Index(chat, "先看一下文件") > strings.Index(chat, "Read a.go") {
		t.Errorf("assistant text should render above the tool entry:\n%s", chat)
	}
}

func TestToolCallBeginFlushesWithoutTextComplete(t *testing.T) {
	// Without TextComplete the text stays buffered until turn end; the tool
	// entry still renders (display order may invert, history stays correct).
	fake := &fakeProvider{scripts: [][]llm.StreamEvent{
		{
			llm.TextDelta{Text: "fallback"},
			llm.ToolCallComplete{ToolID: "tu_1", ToolName: "read_file",
				Arguments: map[string]any{"file_path": "a.go"}},
			llm.StreamEnd{},
		},
		{llm.StreamEnd{}},
	}}
	m := send(t, newTestModel(fake), "q")
	if len(m.entries) != 4 {
		t.Fatalf("entries = %d, want 4 (user, tool, assistant, turn)", len(m.entries))
	}
	if m.entries[2].kind != entryAssistant || m.entries[2].text != "fallback" {
		t.Errorf("entries[2] = %+v, want assistant flushed at turn end", m.entries[2])
	}
}
