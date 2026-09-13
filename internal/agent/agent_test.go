package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/tools"
)

// fakeClient replays one scripted event stream per request (index-matched),
// falling back to the last script for extra requests, and records every
// request it saw.
type fakeClient struct {
	scripts [][]llm.StreamEvent
	reqs    []llm.Request
}

func (f *fakeClient) Stream(_ context.Context, req llm.Request) <-chan llm.StreamEvent {
	f.reqs = append(f.reqs, req)
	idx := len(f.reqs) - 1
	if idx >= len(f.scripts) {
		idx = len(f.scripts) - 1 // extra requests replay the last script
	}
	var script []llm.StreamEvent
	if idx >= 0 {
		script = f.scripts[idx]
	}
	ch := make(chan llm.StreamEvent, len(script))
	for _, ev := range script {
		ch <- ev
	}
	close(ch)
	return ch
}

// blockingClient emits one event, then hangs until ctx is cancelled and ends
// with a StreamError carrying ctx.Err() — the shape a cancelled real request
// produces.
type blockingClient struct {
	first   llm.StreamEvent
	started chan struct{}
}

func (b *blockingClient) Stream(ctx context.Context, _ llm.Request) <-chan llm.StreamEvent {
	ch := make(chan llm.StreamEvent)
	go func() {
		defer close(ch)
		ch <- b.first
		close(b.started)
		<-ctx.Done()
		ch <- llm.StreamError{Err: ctx.Err()}
	}()
	return ch
}

// recordingTool remembers the args it was executed with; delay simulates a
// slow tool so out-of-order completion can be staged.
type recordingTool struct {
	name  string
	cat   tools.Category
	delay time.Duration
	seen  []map[string]any
}

func newRecordingTool(name string, cat tools.Category) *recordingTool {
	return &recordingTool{name: name, cat: cat}
}

func (t *recordingTool) Name() string             { return t.name }
func (t *recordingTool) Description() string      { return "records args" }
func (t *recordingTool) Category() tools.Category { return t.cat }
func (t *recordingTool) Schema() map[string]any {
	return map[string]any{"name": t.Name(), "description": t.Description(), "input_schema": map[string]any{"type": "object"}}
}
func (t *recordingTool) Execute(_ context.Context, args map[string]any) tools.ToolResult {
	if t.delay > 0 {
		time.Sleep(t.delay)
	}
	t.seen = append(t.seen, args)
	return tools.ToolResult{Output: "echo ok"}
}

func toolCall(id, name string, args map[string]any) llm.StreamEvent {
	return llm.ToolCallComplete{ToolID: id, ToolName: name, Arguments: args}
}

// collectLoop drains one loop and verifies the terminal contract: exactly one
// terminal event (LoopComplete or TurnError), and it is last.
func collectLoop(t *testing.T, o *Orchestrator, text string) []AgentEvent {
	t.Helper()
	return drainLoop(t, o.RunLoop(context.Background(), text))
}

func collectLoopCtx(t *testing.T, o *Orchestrator, ctx context.Context, text string) []AgentEvent {
	t.Helper()
	return drainLoop(t, o.RunLoop(ctx, text))
}

func drainLoop(t *testing.T, ch <-chan AgentEvent) []AgentEvent {
	t.Helper()
	var evs []AgentEvent
	terminals := 0
	for ev := range ch {
		switch ev.(type) {
		case LoopComplete, TurnError:
			terminals++
			if terminals > 1 {
				t.Fatalf("second terminal event: %+v (events so far: %d)", ev, len(evs))
			}
		}
		evs = append(evs, ev)
	}
	if terminals != 1 {
		t.Fatalf("loop produced %d terminal events, want exactly 1 (events: %d)", terminals, len(evs))
	}
	return evs
}

func lastEvent(evs []AgentEvent) AgentEvent { return evs[len(evs)-1] }

func newTestOrchestrator(t *testing.T, client llm.Client, maxRounds int) (*Orchestrator, *conversation.Manager) {
	t.Helper()
	conv := conversation.NewManager()
	return New(client, tools.NewRegistry(), conv, "sys", false, maxRounds), conv
}

func TestRunLoopEndToEndWithToolCall(t *testing.T) {
	tool := newRecordingTool("echo_tool", tools.CategoryRead)
	reg := tools.NewRegistry()
	reg.Register(tool)
	conv := conversation.NewManager()
	fc := &fakeClient{scripts: [][]llm.StreamEvent{
		{
			llm.TextDelta{Text: "checking "},
			llm.TextDelta{Text: "the file"},
			toolCall("tu_1", "echo_tool", map[string]any{"path": "a.go"}),
			llm.StreamEnd{StopReason: "tool_use", Usage: llm.UsageInfo{InputTokens: 10, OutputTokens: 5}},
		},
		{
			llm.TextDelta{Text: "all good"},
			llm.StreamEnd{StopReason: "end_turn", Usage: llm.UsageInfo{InputTokens: 20, OutputTokens: 3}},
		},
	}}
	o := New(fc, reg, conv, "sys", false, 50)

	evs := collectLoop(t, o, "please look")

	// Event sequence: text deltas, begin/end pair, turn progress, second
	// round's text, then the single terminal.
	want := []string{"TextDelta", "TextDelta", "ToolCallBegin", "ToolCallEnd", "TurnComplete", "TextDelta", "LoopComplete"}
	if len(evs) != len(want) {
		t.Fatalf("events = %+v (%d), want %v", evs, len(evs), want)
	}
	for i, w := range want {
		if got := typeName(evs[i]); got != w {
			t.Errorf("evs[%d] = %s, want %s (%+v)", i, got, w, evs[i])
		}
	}
	if td, ok := evs[0].(TextDelta); !ok || td.Text != "checking " {
		t.Errorf("evs[0] = %+v", evs[0])
	}
	begin, ok := evs[2].(ToolCallBegin)
	if !ok || begin.ToolID != "tu_1" || begin.ToolName != "echo_tool" || begin.Args["path"] != "a.go" {
		t.Errorf("evs[2] = %+v", evs[2])
	}
	end, ok := evs[3].(ToolCallEnd)
	if !ok || end.ToolID != "tu_1" || end.Result.Output != "echo ok" || end.Result.IsError {
		t.Errorf("evs[3] = %+v", evs[3])
	}
	if tc, ok := evs[4].(TurnComplete); !ok || tc.Turn != 1 {
		t.Errorf("evs[4] = %+v, want TurnComplete{1}", evs[4])
	}
	final, ok := lastEvent(evs).(LoopComplete)
	if !ok || final.Outcome != OutcomeModelStop || final.TotalTurns != 1 {
		t.Fatalf("terminal = %+v", lastEvent(evs))
	}
	if final.Usage != (llm.UsageInfo{InputTokens: 30, OutputTokens: 8}) {
		t.Errorf("terminal usage = %+v, want sum of both rounds", final.Usage)
	}

	// The tool saw the model's args.
	if len(tool.seen) != 1 || tool.seen[0]["path"] != "a.go" {
		t.Errorf("tool args = %+v", tool.seen)
	}

	// History: user → assistant(text + tool_use) → user(tool_result, paired)
	// → assistant(pure reply).
	msgs := conv.GetMessages()
	if len(msgs) != 4 {
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
	if msgs[3].Role != conversation.RoleAssistant || msgs[3].Content != "all good" {
		t.Errorf("msgs[3] = %+v", msgs[3])
	}

	// The second request carried the full history and the tool list.
	if len(fc.reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(fc.reqs))
	}
	if len(fc.reqs[0].Messages) != 1 {
		t.Errorf("first request messages = %d, want 1", len(fc.reqs[0].Messages))
	}
	if len(fc.reqs[1].Messages) != 3 {
		t.Errorf("second request messages = %d, want 3", len(fc.reqs[1].Messages))
	}
	if len(fc.reqs[1].Tools) != 1 || fc.reqs[1].Tools[0]["name"] != "echo_tool" {
		t.Errorf("request tools = %+v", fc.reqs[1].Tools)
	}
}

// typeName gives a short readable name for an event value.
func typeName(ev AgentEvent) string {
	switch ev.(type) {
	case TextDelta:
		return "TextDelta"
	case TextComplete:
		return "TextComplete"
	case ThinkingDelta:
		return "ThinkingDelta"
	case ThinkingComplete:
		return "ThinkingComplete"
	case ToolCallBegin:
		return "ToolCallBegin"
	case ToolCallEnd:
		return "ToolCallEnd"
	case TurnComplete:
		return "TurnComplete"
	case LoopComplete:
		return "LoopComplete"
	case TurnError:
		return "TurnError"
	default:
		return "unknown"
	}
}

func TestRunLoopPlainReply(t *testing.T) {
	fc := &fakeClient{scripts: [][]llm.StreamEvent{
		{
			llm.TextDelta{Text: "just text"},
			llm.StreamEnd{StopReason: "end_turn"},
		},
	}}
	o, conv := newTestOrchestrator(t, fc, 50)
	evs := collectLoop(t, o, "hi")

	if len(evs) != 2 {
		t.Fatalf("events = %+v, want [TextDelta LoopComplete]", evs)
	}
	if _, ok := evs[0].(TextDelta); !ok {
		t.Errorf("evs[0] = %T", evs[0])
	}
	// A pure reply is not a turn: no TurnComplete, and TotalTurns is 0.
	lc, ok := evs[1].(LoopComplete)
	if !ok || lc.Outcome != OutcomeModelStop || lc.TotalTurns != 0 {
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

func TestRunLoopMultiRoundTurnCounting(t *testing.T) {
	tool := newRecordingTool("echo_tool", tools.CategoryRead)
	reg := tools.NewRegistry()
	reg.Register(tool)
	fc := &fakeClient{scripts: [][]llm.StreamEvent{
		{toolCall("tu_1", "echo_tool", nil), llm.StreamEnd{StopReason: "tool_use"}},
		{toolCall("tu_2", "echo_tool", nil), llm.StreamEnd{StopReason: "tool_use"}},
		{llm.TextDelta{Text: "done"}, llm.StreamEnd{StopReason: "end_turn"}},
	}}
	o, _ := newTestOrchestrator(t, fc, 50)
	o.registry.Register(tool)
	evs := collectLoop(t, o, "work")

	var turns []int
	for _, ev := range evs {
		if tc, ok := ev.(TurnComplete); ok {
			turns = append(turns, tc.Turn)
		}
	}
	if len(turns) != 2 || turns[0] != 1 || turns[1] != 2 {
		t.Errorf("TurnComplete turns = %v, want [1 2]", turns)
	}
	if fc.reqs[2].Messages[0].Content != "work" {
		t.Errorf("history lost the user message: %+v", fc.reqs[2].Messages[0])
	}
	final := lastEvent(evs).(LoopComplete)
	if final.Outcome != OutcomeModelStop || final.TotalTurns != 2 {
		t.Errorf("terminal = %+v, want ModelStop with 2 turns", final)
	}
}

func TestRunLoopRecordsThinkingInHistory(t *testing.T) {
	// Thinking blocks are committed on tool turns, where replaying them (with
	// the provider's signature) is required to continue the thinking chain.
	fc := &fakeClient{scripts: [][]llm.StreamEvent{
		{
			llm.ThinkingDelta{Text: "hmm"},
			llm.ThinkingComplete{Thinking: "hmm", Signature: "sig_1"},
			llm.TextDelta{Text: "calling"},
			toolCall("tu_1", "echo_tool", nil),
			llm.StreamEnd{StopReason: "tool_use"},
		},
		{llm.StreamEnd{StopReason: "end_turn"}},
	}}
	tool := newRecordingTool("echo_tool", tools.CategoryRead)
	reg := tools.NewRegistry()
	reg.Register(tool)
	conv := conversation.NewManager()
	o := New(fc, reg, conv, "sys", false, 50)
	collectLoop(t, o, "hi")

	msgs := conv.GetMessages()
	if len(msgs) != 3 {
		t.Fatalf("history = %+v", msgs)
	}
	th := msgs[1].ThinkingBlocks
	if len(th) != 1 || th[0].Text != "hmm" || th[0].Signature != "sig_1" {
		t.Errorf("thinking blocks = %+v, want one with signature sig_1", th)
	}
}

func TestRunLoopUnknownToolGetsErrorResult(t *testing.T) {
	fc := &fakeClient{scripts: [][]llm.StreamEvent{
		{
			toolCall("tu_x", "no_such_tool", nil),
			llm.StreamEnd{StopReason: "tool_use"},
		},
		{llm.StreamEnd{StopReason: "end_turn"}},
	}}
	o, conv := newTestOrchestrator(t, fc, 50)
	evs := collectLoop(t, o, "do the thing")

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
	// One unknown call is below the limit: the loop continued and stopped
	// normally on the model's own reply.
	final := lastEvent(evs).(LoopComplete)
	if final.Outcome != OutcomeModelStop || final.TotalTurns != 1 {
		t.Errorf("terminal = %+v, want ModelStop after recovery", final)
	}
}

func TestRunLoopStreamError(t *testing.T) {
	fc := &fakeClient{scripts: [][]llm.StreamEvent{
		{
			llm.TextDelta{Text: "partial"},
			llm.StreamError{Err: context.DeadlineExceeded},
		},
	}}
	o, conv := newTestOrchestrator(t, fc, 50)
	evs := collectLoop(t, o, "hi")

	last := lastEvent(evs)
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

func TestRunLoopMultipleToolCallsInOrder(t *testing.T) {
	tool := newRecordingTool("echo_tool", tools.CategoryRead)
	reg := tools.NewRegistry()
	reg.Register(tool)
	conv := conversation.NewManager()
	fc := &fakeClient{scripts: [][]llm.StreamEvent{
		{
			toolCall("tu_1", "echo_tool", map[string]any{"n": 1.0}),
			toolCall("tu_2", "echo_tool", map[string]any{"n": 2.0}),
			llm.StreamEnd{StopReason: "tool_use"},
		},
		{llm.StreamEnd{StopReason: "end_turn"}},
	}}
	o := New(fc, reg, conv, "sys", false, 50)
	evs := collectLoop(t, o, "two calls")

	// Ends fire in the model's original call order regardless of execution
	// timing; both are read-category so they share one concurrent batch.
	var ends []ToolCallEnd
	for _, ev := range evs {
		if e, ok := ev.(ToolCallEnd); ok {
			ends = append(ends, e)
		}
	}
	if len(ends) != 2 || ends[0].ToolID != "tu_1" || ends[1].ToolID != "tu_2" {
		t.Fatalf("ends = %+v", ends)
	}
	// Execution order within a concurrent batch is nondeterministic; the
	// ordering invariant covers results — asserted via ends above and the
	// history below.
	if len(tool.seen) != 2 {
		t.Fatalf("tool executions = %d, want 2", len(tool.seen))
	}
	seenN := map[float64]bool{}
	for _, args := range tool.seen {
		seenN[args["n"].(float64)] = true
	}
	if !seenN[1.0] || !seenN[2.0] {
		t.Errorf("tool args = %+v, want both calls executed", tool.seen)
	}
	msgs := conv.GetMessages()
	if len(msgs[2].ToolResults) != 2 {
		t.Fatalf("results message = %+v, want 2 results in one message", msgs[2])
	}
	if msgs[2].ToolResults[0].ToolUseID != "tu_1" {
		t.Errorf("results[0] = %+v", msgs[2].ToolResults[0])
	}
}

func TestRunLoopMaxIterationsSynthesizesResults(t *testing.T) {
	tool := newRecordingTool("echo_tool", tools.CategoryRead)
	reg := tools.NewRegistry()
	reg.Register(tool)
	fc := &fakeClient{scripts: [][]llm.StreamEvent{
		{toolCall("tu_1", "echo_tool", nil), llm.StreamEnd{StopReason: "tool_use"}},
		{toolCall("tu_1", "echo_tool", nil), llm.StreamEnd{StopReason: "tool_use"}},
	}}
	o, conv := newTestOrchestrator(t, fc, 1)
	o.registry.Register(tool)
	evs := collectLoop(t, o, "keep going")

	// The cap counts executed rounds: round 1 ran; round 2's response was
	// received but its calls never executed.
	if len(fc.reqs) != 2 {
		t.Fatalf("requests = %d, want 2 (round 2 answered, then the cap fired)", len(fc.reqs))
	}
	final := lastEvent(evs).(LoopComplete)
	if final.Outcome != OutcomeMaxIterations || final.TotalTurns != 1 {
		t.Fatalf("terminal = %+v, want MaxIterations with 1 turn", final)
	}

	// History stays pair-complete: the unexecuted call got a synthetic
	// failure result explaining why.
	msgs := conv.GetMessages()
	if len(msgs) != 5 {
		t.Fatalf("history len = %d: %+v", len(msgs), msgs)
	}
	if len(msgs[3].ToolUses) != 1 || msgs[3].ToolUses[0].ID != "tu_1" {
		t.Fatalf("unexecuted assistant turn = %+v", msgs[3])
	}
	synth := msgs[4].ToolResults
	if len(synth) != 1 || synth[0].ToolUseID != "tu_1" || !synth[0].IsError ||
		!strings.Contains(synth[0].Content, "iteration limit") {
		t.Errorf("synthetic result = %+v", synth)
	}
}

func TestRunLoopCancelKeepsPartialHistoryLegal(t *testing.T) {
	// Stream: one announced tool call, then hang until cancelled. The loop
	// must end with LoopComplete{Cancelled} — not TurnError — with the
	// announced-but-unexecuted call repaired by a synthetic result.
	fc := &blockingClient{first: toolCall("tu_1", "echo_tool", nil), started: make(chan struct{})}
	reg := tools.NewRegistry()
	reg.Register(newRecordingTool("echo_tool", tools.CategoryRead))
	conv := conversation.NewManager()
	o := New(fc, reg, conv, "sys", false, 50)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-fc.started
		cancel()
	}()
	evs := collectLoopCtx(t, o, ctx, "go")

	final := lastEvent(evs).(LoopComplete)
	if final.Outcome != OutcomeCancelled {
		t.Fatalf("terminal = %+v, want Cancelled", final)
	}
	for _, ev := range evs {
		if _, ok := ev.(TurnError); ok {
			t.Errorf("cancel surfaced as TurnError: %+v", ev)
		}
	}

	// Partial assistant message committed, synthetic result keeps pairing.
	msgs := conv.GetMessages()
	if len(msgs) != 3 {
		t.Fatalf("history len = %d: %+v", len(msgs), msgs)
	}
	if len(msgs[1].ToolUses) != 1 || msgs[1].ToolUses[0].ID != "tu_1" {
		t.Fatalf("partial assistant turn = %+v", msgs[1])
	}
	synth := msgs[2].ToolResults
	if len(synth) != 1 || synth[0].ToolUseID != "tu_1" || !synth[0].IsError ||
		!strings.Contains(synth[0].Content, "cancelled") {
		t.Errorf("synthetic result = %+v", synth)
	}
}

func TestRunLoopUnknownStreakStopsLoop(t *testing.T) {
	// Three consecutive unknown calls in one round: the loop stops after the
	// results are committed, before the next request.
	fc := &fakeClient{scripts: [][]llm.StreamEvent{
		{
			toolCall("tu_1", "nope", nil),
			toolCall("tu_2", "nah", nil),
			toolCall("tu_3", "nada", nil),
			llm.StreamEnd{StopReason: "tool_use"},
		},
	}}
	o, conv := newTestOrchestrator(t, fc, 50)
	evs := collectLoop(t, o, "call things")

	final := lastEvent(evs).(LoopComplete)
	if final.Outcome != OutcomeUnknownTool || final.TotalTurns != 1 {
		t.Fatalf("terminal = %+v, want UnknownTool with 1 turn", final)
	}
	if len(fc.reqs) != 1 {
		t.Errorf("requests = %d, want 1 (stop before the next request)", len(fc.reqs))
	}
	// All three got error results so history stays pair-complete.
	msgs := conv.GetMessages()
	if len(msgs) != 3 || len(msgs[2].ToolResults) != 3 {
		t.Fatalf("history = %+v", msgs)
	}
	for i, tr := range msgs[2].ToolResults {
		if !tr.IsError {
			t.Errorf("results[%d] = %+v, want error", i, tr)
		}
	}
}

func TestRunLoopKnownToolResetsStreak(t *testing.T) {
	// unknown → known → unknown → unknown: the known call resets the streak
	// mid-round, so it climbs back to only 2 — below the limit. The loop must
	// NOT stop; it continues to the model's reply in round 2.
	tool := newRecordingTool("echo_tool", tools.CategoryRead)
	reg := tools.NewRegistry()
	reg.Register(tool)
	fc := &fakeClient{scripts: [][]llm.StreamEvent{
		{
			toolCall("tu_1", "nope", nil),
			toolCall("tu_2", "echo_tool", nil),
			toolCall("tu_3", "nope", nil),
			toolCall("tu_4", "nope", nil),
			llm.StreamEnd{StopReason: "tool_use"},
		},
		{llm.StreamEnd{StopReason: "end_turn"}},
	}}
	o, _ := newTestOrchestrator(t, fc, 50)
	o.registry.Register(tool)
	evs := collectLoop(t, o, "try things")

	final := lastEvent(evs).(LoopComplete)
	if final.Outcome != OutcomeModelStop || final.TotalTurns != 1 {
		t.Fatalf("terminal = %+v, want ModelStop (streak was reset)", final)
	}
	if len(fc.reqs) != 2 {
		t.Errorf("requests = %d, want 2", len(fc.reqs))
	}
}

// pairingComplete reports the first tool_use/tool_result pairing violation in
// a request's message list, mirroring EnsureToolPairing's invariant.
func pairingComplete(msgs []conversation.Message) error {
	for i, msg := range msgs {
		if len(msg.ToolUses) == 0 {
			continue
		}
		if i+1 >= len(msgs) {
			return fmt.Errorf("message %d carries tool uses but has no successor", i)
		}
		got := map[string]bool{}
		for _, tr := range msgs[i+1].ToolResults {
			got[tr.ToolUseID] = true
		}
		for _, tu := range msg.ToolUses {
			if !got[tu.ID] {
				return fmt.Errorf("tool use %s (%s) has no result", tu.ID, tu.Name)
			}
		}
	}
	return nil
}

func TestRunLoopOutOfOrderCompletionKeepsOriginalOrderTrio(t *testing.T) {
	// The slow first call finishes last, yet ToolCallEnd events, the history
	// results, and the model's call order must all agree, index by index.
	slow := newRecordingTool("slow_tool", tools.CategoryRead)
	slow.delay = 80 * time.Millisecond
	fast := newRecordingTool("fast_tool", tools.CategoryRead)
	reg := tools.NewRegistry()
	reg.Register(slow)
	reg.Register(fast)
	fc := &fakeClient{scripts: [][]llm.StreamEvent{
		{
			toolCall("tu_1", "slow_tool", nil),
			toolCall("tu_2", "fast_tool", nil),
			llm.StreamEnd{StopReason: "tool_use"},
		},
		{llm.StreamEnd{StopReason: "end_turn"}},
	}}
	o, conv := newTestOrchestrator(t, fc, 50)
	o.registry.Register(slow)
	o.registry.Register(fast)
	evs := collectLoop(t, o, "race")

	var ends []ToolCallEnd
	for _, ev := range evs {
		if e, ok := ev.(ToolCallEnd); ok {
			ends = append(ends, e)
		}
	}
	if len(ends) != 2 || ends[0].ToolID != "tu_1" || ends[1].ToolID != "tu_2" {
		t.Fatalf("ToolCallEnd order = %+v, want original call order [tu_1 tu_2]", ends)
	}
	results := conv.GetMessages()[2].ToolResults
	if len(results) != 2 || results[0].ToolUseID != "tu_1" || results[1].ToolUseID != "tu_2" {
		t.Fatalf("history results = %+v, want original call order", results)
	}
}

func TestRunLoopPostAbortHistoryStaysPairComplete(t *testing.T) {
	// After the cap aborts a loop (synthetic results committed), the next
	// conversation turn's request must carry fully paired history.
	tool := newRecordingTool("echo_tool", tools.CategoryRead)
	reg := tools.NewRegistry()
	reg.Register(tool)
	fc := &fakeClient{scripts: [][]llm.StreamEvent{
		{toolCall("tu_1", "echo_tool", nil), llm.StreamEnd{StopReason: "tool_use"}},
		{toolCall("tu_1", "echo_tool", nil), llm.StreamEnd{StopReason: "tool_use"}},
		{llm.TextDelta{Text: "recovered"}, llm.StreamEnd{StopReason: "end_turn"}},
	}}
	o, _ := newTestOrchestrator(t, fc, 1)
	o.registry.Register(tool)

	evs := collectLoop(t, o, "first")
	if last := lastEvent(evs); last.(LoopComplete).Outcome != OutcomeMaxIterations {
		t.Fatalf("first loop terminal = %+v, want MaxIterations", last)
	}
	evs = collectLoop(t, o, "second")
	if last := lastEvent(evs); last.(LoopComplete).Outcome != OutcomeModelStop {
		t.Fatalf("second loop terminal = %+v, want ModelStop", last)
	}
	// reqs[2] is the follow-up request made after the aborted loop.
	if err := pairingComplete(fc.reqs[2].Messages); err != nil {
		t.Errorf("post-abort request history illegal: %v", err)
	}
	if msgs := fc.reqs[2].Messages; msgs[len(msgs)-1].Content != "second" {
		t.Errorf("follow-up request lost the new user message: %+v", msgs[len(msgs)-1])
	}
}

func TestRunLoopRealToolsReadEditLoop(t *testing.T) {
	// Integration: real read_file → edit_file against a temp dir, driven by
	// RunLoop — the read satisfies the edit gate, the file changes on disk.
	dir := t.TempDir()
	file := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(file, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fsc := tools.NewFileStateCache()
	reg := tools.NewRegistry()
	reg.Register(&tools.ReadFileTool{WorkDir: dir, FileStateCache: fsc})
	reg.Register(&tools.EditFileTool{WorkDir: dir, FileStateCache: fsc})
	fc := &fakeClient{scripts: [][]llm.StreamEvent{
		{
			toolCall("tu_1", "read_file", map[string]any{"file_path": "note.txt"}),
			llm.StreamEnd{StopReason: "tool_use"},
		},
		{
			toolCall("tu_2", "edit_file", map[string]any{
				"file_path": "note.txt", "old_string": "alpha", "new_string": "beta",
			}),
			llm.StreamEnd{StopReason: "tool_use"},
		},
		{llm.TextDelta{Text: "edited"}, llm.StreamEnd{StopReason: "end_turn"}},
	}}
	o, conv := newTestOrchestrator(t, fc, 50)
	o.registry = reg
	evs := collectLoop(t, o, "read then edit")

	final := lastEvent(evs).(LoopComplete)
	if final.Outcome != OutcomeModelStop || final.TotalTurns != 2 {
		t.Fatalf("terminal = %+v, want ModelStop after 2 turns", final)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "beta\n" {
		t.Fatalf("file = %q err = %v, want the edit applied", data, err)
	}
	// Both real results landed in history, in call order, without error.
	results := conv.GetMessages()[4].ToolResults
	if len(results) != 1 || results[0].IsError || !strings.Contains(results[0].Content, "beta") {
		t.Fatalf("edit result = %+v", results)
	}
	if err := pairingComplete(fc.reqs[2].Messages); err != nil {
		t.Errorf("request history illegal: %v", err)
	}
}

func TestRunLoopPlanModeFiltersToolsAndPrompt(t *testing.T) {
	read := newRecordingTool("echo_tool", tools.CategoryRead)
	write := newRecordingTool("write_tool", tools.CategoryWrite)
	reg := tools.NewRegistry()
	reg.Register(read)
	reg.Register(write)
	fc := &fakeClient{scripts: [][]llm.StreamEvent{
		{llm.StreamEnd{StopReason: "end_turn"}},
	}}
	conv := conversation.NewManager()
	o := New(fc, reg, conv, "sys", false, 50)
	o.SetMode(ModePlan)
	if o.Mode() != ModePlan {
		t.Fatalf("Mode() = %s, want plan", o.Mode())
	}
	collectLoop(t, o, "make a plan")

	req := fc.reqs[0]
	if !strings.Contains(req.System, "PLAN MODE") {
		t.Errorf("plan-mode system prompt missing segment: %q", req.System)
	}
	if !strings.HasPrefix(req.System, "sys") {
		t.Errorf("plan-mode system prompt lost the base prompt: %q", req.System)
	}
	if len(req.Tools) != 1 || req.Tools[0]["name"] != "echo_tool" {
		t.Errorf("plan-mode tools = %+v, want only the read tool", req.Tools)
	}

	// Back to normal mode: full tool set, base prompt.
	o.SetMode(ModeNormal)
	collectLoop(t, o, "go")
	req = fc.reqs[1]
	if strings.Contains(req.System, "PLAN MODE") {
		t.Errorf("normal-mode system prompt carries the plan segment: %q", req.System)
	}
	if len(req.Tools) != 2 {
		t.Errorf("normal-mode tools = %+v, want both tools", req.Tools)
	}
}
