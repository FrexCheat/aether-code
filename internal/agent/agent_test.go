package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/FrexCheat/aether-code/internal/compaction"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/tools"
)

type scriptedClient struct {
	mu      sync.Mutex
	streams [][]llm.StreamEvent
	calls   int
}

func (c *scriptedClient) Stream(ctx context.Context, _ llm.Request) <-chan llm.StreamEvent {
	c.mu.Lock()
	events := []llm.StreamEvent{}
	if c.calls < len(c.streams) {
		events = c.streams[c.calls]
	}
	c.calls++
	c.mu.Unlock()

	ch := make(chan llm.StreamEvent, len(events))
	for _, event := range events {
		select {
		case ch <- event:
		case <-ctx.Done():
			close(ch)
			return ch
		}
	}
	close(ch)
	return ch
}

func (c *scriptedClient) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type stubTool struct {
	name     string
	category tools.Category
	calls    atomic.Int32
}

func (t *stubTool) Name() string             { return t.name }
func (t *stubTool) Description() string      { return "test tool" }
func (t *stubTool) Category() tools.Category { return t.category }
func (t *stubTool) Schema() tools.ToolSchema { return tools.ToolSchema{Name: t.name} }
func (t *stubTool) Execute(context.Context, map[string]any) tools.ToolResult {
	t.calls.Add(1)
	return tools.ToolResult{Output: "tool output"}
}

type stubCompactor struct {
	mu     sync.Mutex
	usages []llm.Usage
}

func (c *stubCompactor) Prepare(context.Context, func(compaction.Progress)) {}
func (c *stubCompactor) ForceCompact(context.Context, func(compaction.Progress)) (compaction.Result, error) {
	return compaction.Result{}, nil
}
func (c *stubCompactor) RecordUsage(usage llm.Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.usages = append(c.usages, usage)
}

func (c *stubCompactor) Usages() []llm.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]llm.Usage(nil), c.usages...)
}

func collectEvents(ch <-chan Event) []Event {
	var events []Event
	for event := range ch {
		events = append(events, event)
	}
	return events
}

func TestRunTextTurn(t *testing.T) {
	usage := llm.Usage{InputTokens: 10, OutputTokens: 4}
	client := &scriptedClient{streams: [][]llm.StreamEvent{{
		llm.TextDelta{Text: "hello"},
		llm.TextComplete{},
		llm.StreamEnd{Usage: usage},
	}}}
	compactor := &stubCompactor{}
	conv := conversation.NewManager()
	ag := New(client, tools.NewRegistry(), nil, nil, compactor)

	events := collectEvents(ag.Run(context.Background(), conv))
	if len(events) != 3 {
		t.Fatalf("events = %T, want text delta, text complete and turn complete", events)
	}
	if delta, ok := events[0].(TextDelta); !ok || delta.Text != "hello" {
		t.Errorf("first event = %#v, want text delta", events[0])
	}
	if _, ok := events[1].(TextComplete); !ok {
		t.Errorf("second event = %T, want TextComplete", events[1])
	}
	done, ok := events[2].(LoopComplete)
	if !ok {
		t.Fatalf("last event = %T, want LoopComplete", events[2])
	}
	if done.Reason != StoppedByModel || done.Usage != usage {
		t.Errorf("completion = %+v", done)
	}
	if got := conv.Messages(); len(got) != 1 || got[0].Content != "hello" {
		t.Errorf("history = %+v, want one assistant message", got)
	}
	if usages := compactor.Usages(); len(usages) != 1 || usages[0] != usage {
		t.Errorf("recorded usages = %+v", usages)
	}
}

func TestRunToolRound(t *testing.T) {
	client := &scriptedClient{streams: [][]llm.StreamEvent{
		{
			llm.ToolCallComplete{
				ToolID:    "call-1",
				ToolName:  "read",
				Arguments: map[string]any{"path": "a.txt"},
			},
			llm.StreamEnd{Usage: llm.Usage{InputTokens: 5, OutputTokens: 1}},
		},
		{
			llm.TextDelta{Text: "finished"},
			llm.TextComplete{},
			llm.StreamEnd{Usage: llm.Usage{InputTokens: 8, OutputTokens: 2}},
		},
	}}
	registry := tools.NewRegistry()
	tool := &stubTool{name: "read", category: tools.CategoryRead}
	registry.Register(tool)
	conv := conversation.NewManager()
	ag := New(client, registry, nil, nil, nil)

	events := collectEvents(ag.Run(context.Background(), conv))
	if !containsEvent[ToolCallBegin](events) || !containsEvent[ToolCallEnd](events) {
		t.Fatalf("events = %T, want tool begin and end", events)
	}
	if tool.calls.Load() != 1 {
		t.Errorf("tool calls = %d, want 1", tool.calls.Load())
	}
	if client.Calls() != 2 {
		t.Errorf("stream calls = %d, want 2", client.Calls())
	}
	if got := conv.Messages(); len(got) != 3 {
		t.Errorf("history has %d messages, want assistant, tool result and assistant", len(got))
	}
}

func TestRunPermission(t *testing.T) {
	client := &scriptedClient{streams: [][]llm.StreamEvent{
		{
			llm.ToolCallComplete{
				ToolID:    "call-1",
				ToolName:  "write",
				Arguments: map[string]any{"path": "a.txt"},
			},
			llm.StreamEnd{},
		},
	}}
	registry := tools.NewRegistry()
	tool := &stubTool{name: "write", category: tools.CategoryWrite}
	registry.Register(tool)
	checker, err := permissions.NewChecker(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ag := New(client, registry, checker, nil, nil)
	ch := ag.Run(context.Background(), conversation.NewManager())

	var events []Event
	for event := range ch {
		events = append(events, event)
		if request, ok := event.(PermissionRequestEvent); ok {
			request.Request.Reply <- permissions.AllowOnce
		}
	}
	if !containsEvent[PermissionRequestEvent](events) {
		t.Fatal("turn did not ask for permission")
	}
	if tool.calls.Load() != 1 {
		t.Errorf("tool calls = %d, want 1", tool.calls.Load())
	}
}

func TestRunStreamError(t *testing.T) {
	wantErr := errors.New("provider failed")
	client := &scriptedClient{streams: [][]llm.StreamEvent{{
		llm.StreamError{Err: wantErr},
	}}}
	ag := New(client, tools.NewRegistry(), nil, nil, nil)

	events := collectEvents(ag.Run(context.Background(), conversation.NewManager()))
	if len(events) != 1 {
		t.Fatalf("events = %T, want one failure", events)
	}
	failed, ok := events[0].(LoopError)
	if !ok || !errors.Is(failed.Err, wantErr) {
		t.Errorf("event = %#v, want stream error", events[0])
	}
}

func TestRunCancelledBeforeRequest(t *testing.T) {
	client := &scriptedClient{}
	ag := New(client, tools.NewRegistry(), nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	events := collectEvents(ag.Run(ctx, conversation.NewManager()))
	if len(events) != 1 {
		t.Fatalf("events = %T, want one completion", events)
	}
	done, ok := events[0].(LoopComplete)
	if !ok || done.Reason != StoppedByUser {
		t.Errorf("event = %#v, want cancelled completion", events[0])
	}
}

func containsEvent[T Event](events []Event) bool {
	for _, event := range events {
		if _, ok := event.(T); ok {
			return true
		}
	}
	return false
}
