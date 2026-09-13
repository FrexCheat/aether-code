package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/tools"
)

type callResult struct {
	Result  tools.ToolResult
	Elapsed time.Duration
}

func executeToolCalls(ctx context.Context, calls []conversation.ToolUseBlock, reg *tools.Registry, onDone func(index int, res callResult)) []callResult {
	results := make([]callResult, len(calls))
	notify := func(i int) {
		if onDone != nil {
			onDone(i, results[i])
		}
	}

	for i := 0; i < len(calls); {
		tool := reg.Get(calls[i].Name)
		switch {
		case tool == nil:
			// Unknown tool: instant structured failure, never concurrent.
			results[i] = callResult{
				Result: tools.ToolResult{
					Output:  "unknown tool " + calls[i].Name + "; pick one of the tools offered",
					IsError: true,
				},
			}
			notify(i)
			i++
		case tool.Category() != tools.CategoryRead:
			// Write / command: run alone as a barrier between read batches.
			results[i] = runTool(ctx, tool, calls[i])
			notify(i)
			i++
		default:
			// Consecutive known reads form one concurrent batch.
			j := i + 1
			for j < len(calls) {
				next := reg.Get(calls[j].Name)
				if next == nil || next.Category() != tools.CategoryRead {
					break
				}
				j++
			}
			var wg sync.WaitGroup
			for k := i; k < j; k++ {
				wg.Add(1)
				go func(k int) {
					defer wg.Done()
					results[k] = runTool(ctx, reg.Get(calls[k].Name), calls[k])
					notify(k)
				}(k)
			}
			wg.Wait()
			i = j
		}
	}
	return results
}

func runTool(ctx context.Context, tool tools.Tool, call conversation.ToolUseBlock) (res callResult) {
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			res = callResult{
				Result: tools.ToolResult{
					Output:  fmt.Sprintf("tool %s panicked: %v", call.Name, r),
					IsError: true,
				},
				Elapsed: time.Since(start),
			}
		}
	}()
	return callResult{Result: tool.Execute(ctx, call.Args), Elapsed: time.Since(start)}
}
