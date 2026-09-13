package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/tools"
)

// stubTool is a configurable fake: fixed category, output, optional delay and
// per-execution hook.
type stubTool struct {
	name    string
	cat     tools.Category
	output  string
	isError bool
	delay   time.Duration
	onExec  func()
}

func (t *stubTool) Name() string             { return t.name }
func (t *stubTool) Description() string      { return "stub " + t.name }
func (t *stubTool) Category() tools.Category { return t.cat }
func (t *stubTool) Schema() tools.ToolSchema {
	return tools.ToolSchema{Name: t.name, Description: t.Description(), InputSchema: map[string]any{"type": "object"}}
}
func (t *stubTool) Execute(_ context.Context, _ map[string]any) tools.ToolResult {
	if t.delay > 0 {
		time.Sleep(t.delay)
	}
	if t.onExec != nil {
		t.onExec()
	}
	return tools.ToolResult{Output: t.output, IsError: t.isError}
}

func call(name string, id string) conversation.ToolUseBlock {
	return conversation.ToolUseBlock{ID: id, Name: name, Args: map[string]any{}}
}

// collectOrder wraps onDone into an index recorder.
func collectOrder(order *[]int) func(int, callResult) {
	var mu sync.Mutex
	return func(i int, _ callResult) {
		mu.Lock()
		defer mu.Unlock()
		*order = append(*order, i)
	}
}

// rendezvousTool pairs with a peer tool: it signals its own arrival (buffered,
// never blocks) and waits for the peer's. Only a truly concurrent batch
// completes without the 2s timeout — a serial executor fails loudly here.
type rendezvousTool struct {
	name    string
	peer    chan struct{} // receives the peer's arrival signal
	arrived chan struct{} // buffered(1); the peer receives from it
}

func (t *rendezvousTool) Name() string             { return t.name }
func (t *rendezvousTool) Description() string      { return "rendezvous " + t.name }
func (t *rendezvousTool) Category() tools.Category { return tools.CategoryRead }
func (t *rendezvousTool) Schema() tools.ToolSchema {
	return tools.ToolSchema{Name: t.name, Description: t.Description(), InputSchema: map[string]any{"type": "object"}}
}
func (t *rendezvousTool) Execute(_ context.Context, _ map[string]any) tools.ToolResult {
	t.arrived <- struct{}{}
	select {
	case <-t.peer:
		return tools.ToolResult{Output: "rendezvous ok"}
	case <-time.After(2 * time.Second):
		return tools.ToolResult{Output: "rendezvous timeout (batch was not concurrent)", IsError: true}
	}
}

// TestExecuteReadBatchRunsConcurrently proves two read calls in one batch run
// at the same time: each Execute signals its own arrival then waits for the
// peer's — only a truly concurrent batch passes without the 2s timeout.
func TestExecuteReadBatchRunsConcurrently(t *testing.T) {
	ca, cb := make(chan struct{}, 1), make(chan struct{}, 1)
	reg := tools.NewRegistry()
	reg.Register(&rendezvousTool{name: "peek_a", peer: cb, arrived: ca})
	reg.Register(&rendezvousTool{name: "peek_b", peer: ca, arrived: cb})

	out := executeToolCalls(context.Background(), []conversation.ToolUseBlock{call("peek_a", "1"), call("peek_b", "2")}, reg, nil)
	for i, o := range out {
		if o.Result.IsError {
			t.Fatalf("results[%d] errored (%s): batch was not concurrent", i, o.Result.Output)
		}
	}
}

// TestExecuteWriteIsBarrierBetweenReads proves a write call splits read
// batches: the write starts only after both earlier reads finished, and the
// trailing read starts only after the write finished.
func TestExecuteWriteIsBarrierBetweenReads(t *testing.T) {
	var mu sync.Mutex
	var log []string
	record := func(ev string) func() {
		return func() {
			mu.Lock()
			defer mu.Unlock()
			log = append(log, ev)
		}
	}
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "r1", cat: tools.CategoryRead, delay: 30 * time.Millisecond, onExec: record("r1.end")})
	reg.Register(&stubTool{name: "w1", cat: tools.CategoryWrite, onExec: record("w1.end")})
	reg.Register(&stubTool{name: "r2", cat: tools.CategoryRead, onExec: record("r2.end")})

	executeToolCalls(context.Background(), []conversation.ToolUseBlock{call("r1", "1"), call("w1", "2"), call("r2", "3")}, reg, nil)

	// The log records end-events only, in chronological append order. r1 was
	// delayed 30ms: if the write ran without waiting for the read batch
	// (barrier broken), w1.end would land before r1.end in the log.
	first := strings.Join(log, ",")
	if !strings.HasPrefix(first, "r1.end,w1.end,r2.end") {
		t.Fatalf("timeline = %q, want r1.end,w1.end,r2.end (write must wait for the read batch)", first)
	}
}

// TestExecuteCompletionOrderCallbacksIndexAlignedResults proves onDone fires
// as each call completes (the fast one first) while the returned slice stays
// index-aligned with the input.
func TestExecuteCompletionOrderCallbacksIndexAlignedResults(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "slow", cat: tools.CategoryRead, output: "slow-out", delay: 80 * time.Millisecond})
	reg.Register(&stubTool{name: "fast", cat: tools.CategoryRead, output: "fast-out"})

	var order []int
	out := executeToolCalls(context.Background(),
		[]conversation.ToolUseBlock{call("slow", "1"), call("fast", "2")}, reg, collectOrder(&order))

	if out[0].Result.Output != "slow-out" || out[1].Result.Output != "fast-out" {
		t.Fatalf("results misaligned: %q, %q", out[0].Result.Output, out[1].Result.Output)
	}
	if len(order) != 2 || order[0] != 1 || order[1] != 0 {
		t.Fatalf("onDone order = %v, want completion order [1 0]", order)
	}
}

// TestExecuteUnknownToolFailsInstantlyAndSerially proves an unknown tool gets
// an immediate error res, is never run concurrently, and splits
// surrounding read batches.
func TestExecuteUnknownToolFailsInstantlyAndSerially(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "read_a", cat: tools.CategoryRead, output: "a"})

	var order []int
	out := executeToolCalls(context.Background(),
		[]conversation.ToolUseBlock{call("nope", "1"), call("read_a", "2")}, reg, collectOrder(&order))

	if !out[0].Result.IsError {
		t.Fatalf("results[0] = %+v, want error", out[0])
	}
	if !strings.Contains(out[0].Result.Output, "unknown tool nope") {
		t.Fatalf("output = %q, want unknown-tool message", out[0].Result.Output)
	}
	if out[1].Result.Output != "a" {
		t.Fatalf("results[1] = %+v, want normal read result", out[1])
	}
	if len(order) != 2 || order[0] != 0 || order[1] != 1 {
		t.Fatalf("onDone order = %v, want [0 1]", order)
	}
}

// TestExecuteFailureDoesNotPoisonBatch proves one failing call leaves its
// batch-mates untouched.
func TestExecuteFailureDoesNotPoisonBatch(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "bad", cat: tools.CategoryRead, output: "boom", isError: true})
	reg.Register(&stubTool{name: "good", cat: tools.CategoryRead, output: "fine"})

	out := executeToolCalls(context.Background(),
		[]conversation.ToolUseBlock{call("bad", "1"), call("good", "2")}, reg, nil)

	if !out[0].Result.IsError || out[0].Result.Output != "boom" {
		t.Fatalf("results[0] = %+v, want error boom", out[0])
	}
	if out[1].Result.IsError || out[1].Result.Output != "fine" {
		t.Fatalf("results[1] = %+v, want clean success", out[1])
	}
}
