package agent

import (
	"context"
	"testing"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/tools"
)

// fakeClient replays a scripted event stream and records the request it saw.
type fakeClient struct {
	events  []llm.StreamEvent
	lastReq llm.Request
}

func (f *fakeClient) Stream(_ context.Context, req llm.Request) <-chan llm.StreamEvent {
	f.lastReq = req
	ch := make(chan llm.StreamEvent, len(f.events))
	for _, ev := range f.events {
		ch <- ev
	}
	close(ch)
	return ch
}

// recordingTool remembers the args it was executed with.
type recordingTool struct {
	tool *recordingTool
	seen []map[string]any
}

func newRecordingTool() *recordingTool { return &recordingTool{} }

func (t *recordingTool) Name() string             { return "echo_tool" }
func (t *recordingTool) Description() string      { return "records args" }
func (t *recordingTool) Category() tools.Category { return tools.CategoryRead }
func (t *recordingTool) Schema() map[string]any {
	return map[string]any{"name": t.Name(), "description": t.Description(), "input_schema": map[string]any{"type": "object"}}
}
func (t *recordingTool) Execute(_ context.Context, args map[string]any) tools.ToolResult {
	t.seen = append(t.seen, args)
	return tools.ToolResult{Output: "echo ok"}
}

func collectTurn(t *testing.T, o *Orchestrator, text string) []AgentEvent {
	t.Helper()
	var evs []AgentEvent
	for ev := range o.RunTurn(context.Background(), text) {
		evs = append(evs, ev)
	}
	return evs
}

func newTestOrchestrator(t *testing.T, client llm.Client) (*Orchestrator, *conversation.Manager) {
	t.Helper()
	conv := conversation.NewManager()
	return New(client, tools.NewRegistry(), conv, "sys", false), conv
}

func TestTurnEndToEndWithToolCall(t *testing.T) {
	tool := newRecordingTool()
	reg := tools.NewRegistry()
	reg.Register(tool)
	conv := conversation.NewManager()
	fc := &fakeClient{events: []llm.StreamEvent{
		llm.TextDelta{Text: "checking "},
		llm.TextDelta{Text: "the file"},
		llm.ToolCallStart{ToolName: "echo_tool", ToolID: "tu_1"},
		llm.ToolCallDelta{Text: `{"pa`},
		llm.ToolCallComplete{ToolID: "tu_1", ToolName: "echo_tool",
			Arguments: map[string]any{"path": "a.go"}},
		llm.StreamEnd{StopReason: "tool_use", Usage: llm.UsageInfo{InputTokens: 10, OutputTokens: 5}},
	}}
	o := New(fc, reg, conv, "sys", false)

	evs := collectTurn(t, o, "please look")

	// Event sequence: text deltas, then one begin/end pair, then TurnEnd.
	if len(evs) != 5 {
		t.Fatalf("events = %+v (%d), want 5", evs, len(evs))
	}
	if td, ok := evs[0].(TextDelta); !ok || td.Text != "checking " {
		t.Errorf("evs[0] = %+v", evs[0])
	}
	if td, ok := evs[1].(TextDelta); !ok || td.Text != "the file" {
		t.Errorf("evs[1] = %+v", evs[1])
	}
	begin, ok := evs[2].(ToolCallBegin)
	if !ok || begin.ToolID != "tu_1" || begin.ToolName != "echo_tool" || begin.Args["path"] != "a.go" {
		t.Errorf("evs[2] = %+v", evs[2])
	}
	end, ok := evs[3].(ToolCallEnd)
	if !ok || end.ToolID != "tu_1" || end.Result.Output != "echo ok" || end.Result.IsError {
		t.Errorf("evs[3] = %+v", evs[3])
	}
	turnEnd, ok := evs[4].(TurnEnd)
	if !ok || turnEnd.StopReason != "tool_use" {
		t.Errorf("evs[4] = %+v", evs[4])
	}

	// The tool saw the model's args.
	if len(tool.seen) != 1 || tool.seen[0]["path"] != "a.go" {
		t.Errorf("tool args = %+v", tool.seen)
	}

	// History: user → assistant(text + tool_use) → user(tool_result, paired).
	msgs := conv.GetMessages()
	if len(msgs) != 3 {
		t.Fatalf("history len = %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != conversation.RoleUser || msgs[0].Content != "please look" {
		t.Errorf("msgs[0] = %+v", msgs[0])
	}
	if msgs[1].Role != conversation.RoleAssistant || msgs[1].Content != "checking the file" {
		t.Fatalf("msgs[1] = %+v", msgs[1])
	}
	if len(msgs[1].ToolUses) != 1 {
		t.Fatalf("assistant tool uses = %+v", msgs[1].ToolUses)
	}
	tu := msgs[1].ToolUses[0]
	if tu.ID != "tu_1" || tu.Name != "echo_tool" || tu.Args["path"] != "a.go" {
		t.Errorf("assistant tool_use = %#v", tu)
	}
	if msgs[2].Role != conversation.RoleUser || len(msgs[2].ToolResults) != 1 {
		t.Fatalf("msgs[2] = %+v", msgs[2])
	}
	tr := msgs[2].ToolResults[0]
	if tr.ToolUseID != "tu_1" || tr.Content != "echo ok" || tr.IsError {
		t.Errorf("tool_result = %#v", tr)
	}

	// The next request carries history + tools.
	if len(fc.lastReq.Messages) != 0 {
		// lastReq was captured at stream time (only the user msg existed).
		if len(fc.lastReq.Messages) != 1 {
			t.Errorf("request at stream time carried %d messages, want 1", len(fc.lastReq.Messages))
		}
	}
	if len(fc.lastReq.Tools) != 1 || fc.lastReq.Tools[0]["name"] != "echo_tool" {
		t.Errorf("request tools = %+v", fc.lastReq.Tools)
	}
}

func TestTurnPlainReply(t *testing.T) {
	fc := &fakeClient{events: []llm.StreamEvent{
		llm.TextDelta{Text: "just text"},
		llm.StreamEnd{StopReason: "end_turn"},
	}}
	o, conv := newTestOrchestrator(t, fc)
	evs := collectTurn(t, o, "hi")

	if len(evs) != 2 {
		t.Fatalf("events = %+v, want [TextDelta TurnEnd]", evs)
	}
	if _, ok := evs[0].(TextDelta); !ok {
		t.Errorf("evs[0] = %T", evs[0])
	}
	if te, ok := evs[1].(TurnEnd); !ok || te.StopReason != "end_turn" {
		t.Errorf("evs[1] = %+v", evs[1])
	}
	msgs := conv.GetMessages()
	if len(msgs) != 2 || msgs[1].Role != conversation.RoleAssistant || msgs[1].Content != "just text" {
		t.Fatalf("history = %+v", msgs)
	}
	if msgs[1].ThinkingBlocks != nil || msgs[1].ToolUses != nil {
		t.Errorf("plain reply should carry no thinking/tool uses: %+v", msgs[1])
	}
}

func TestTurnRecordsThinkingInHistory(t *testing.T) {
	// Thinking blocks are committed on tool turns, where replaying them (with
	// the provider's signature) is required to continue the thinking chain.
	fc := &fakeClient{events: []llm.StreamEvent{
		llm.ThinkingDelta{Text: "hmm"},
		llm.ThinkingComplete{Thinking: "hmm", Signature: "sig_1"},
		llm.TextDelta{Text: "calling"},
		llm.ToolCallComplete{ToolID: "tu_1", ToolName: "echo_tool", Arguments: map[string]any{}},
		llm.StreamEnd{StopReason: "tool_use"},
	}}
	tool := newRecordingTool()
	reg := tools.NewRegistry()
	reg.Register(tool)
	conv := conversation.NewManager()
	o := New(fc, reg, conv, "sys", false)
	collectTurn(t, o, "hi")

	msgs := conv.GetMessages()
	if len(msgs) != 3 {
		t.Fatalf("history = %+v", msgs)
	}
	th := msgs[1].ThinkingBlocks
	if len(th) != 1 || th[0].Text != "hmm" || th[0].Signature != "sig_1" {
		t.Errorf("thinking blocks = %+v, want one with signature sig_1", th)
	}
}

func TestTurnUnknownToolGetsErrorResult(t *testing.T) {
	fc := &fakeClient{events: []llm.StreamEvent{
		llm.ToolCallComplete{ToolID: "tu_x", ToolName: "no_such_tool",
			Arguments: map[string]any{}},
		llm.StreamEnd{StopReason: "tool_use"},
	}}
	o, conv := newTestOrchestrator(t, fc)
	evs := collectTurn(t, o, "do the thing")

	var end ToolCallEnd
	for _, ev := range evs {
		if e, ok := ev.(ToolCallEnd); ok {
			end = e
		}
	}
	if end.ToolName != "no_such_tool" || !end.Result.IsError {
		t.Fatalf("expected error ToolCallEnd, got %+v", end)
	}
	msgs := conv.GetMessages()
	if len(msgs) != 3 {
		t.Fatalf("history len = %d, want 3 (pairing preserved)", len(msgs))
	}
	if len(msgs[2].ToolResults) != 1 {
		t.Fatalf("results message = %+v", msgs[2])
	}
	tr := msgs[2].ToolResults[0]
	if tr.ToolUseID != "tu_x" || !tr.IsError {
		t.Errorf("tool_result = %#v", tr)
	}
}

func TestTurnStreamError(t *testing.T) {
	fc := &fakeClient{events: []llm.StreamEvent{
		llm.TextDelta{Text: "partial"},
		llm.StreamError{Err: context.DeadlineExceeded},
	}}
	o, conv := newTestOrchestrator(t, fc)
	evs := collectTurn(t, o, "hi")

	last := evs[len(evs)-1]
	te, ok := last.(TurnError)
	if !ok || te.Err != context.DeadlineExceeded {
		t.Fatalf("last = %+v, want TurnError", last)
	}
	// Nothing committed: the user message was added but no assistant turn.
	msgs := conv.GetMessages()
	if len(msgs) != 1 || msgs[0].Role != conversation.RoleUser {
		t.Fatalf("history after error = %+v", msgs)
	}
}

func TestTurnMultipleToolCallsInOrder(t *testing.T) {
	tool := newRecordingTool()
	reg := tools.NewRegistry()
	reg.Register(tool)
	conv := conversation.NewManager()
	fc := &fakeClient{events: []llm.StreamEvent{
		llm.ToolCallComplete{ToolID: "tu_1", ToolName: "echo_tool", Arguments: map[string]any{"n": 1.0}},
		llm.ToolCallComplete{ToolID: "tu_2", ToolName: "echo_tool", Arguments: map[string]any{"n": 2.0}},
		llm.StreamEnd{StopReason: "tool_use"},
	}}
	o := New(fc, reg, conv, "sys", false)
	evs := collectTurn(t, o, "two calls")

	// Both begin events precede both end events (stream phase), then ends
	// fire in call order.
	var ends []ToolCallEnd
	for _, ev := range evs {
		if e, ok := ev.(ToolCallEnd); ok {
			ends = append(ends, e)
		}
	}
	if len(ends) != 2 || ends[0].ToolID != "tu_1" || ends[1].ToolID != "tu_2" {
		t.Fatalf("ends = %+v", ends)
	}
	if len(tool.seen) != 2 || tool.seen[0]["n"] != 1.0 || tool.seen[1]["n"] != 2.0 {
		t.Errorf("tool call order = %+v", tool.seen)
	}
	msgs := conv.GetMessages()
	if len(msgs[2].ToolResults) != 2 {
		t.Fatalf("results message = %+v, want 2 results in one message", msgs[2])
	}
	if msgs[2].ToolResults[0].ToolUseID != "tu_1" {
		t.Errorf("results[0] = %+v", msgs[2].ToolResults[0])
	}
}
