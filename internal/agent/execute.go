// 分批执行器：把一个 Turn 内的全部工具调用按安全类别调度执行，对上层
// （RunLoop）隐藏并发细节。
//
// 批次规则（对齐 docs/specs/agent-loop/plan.md）：
//   - 连续的已知读类调用合为一批并发执行（WaitGroup join 作屏障）；
//   - 写类 / 命令类调用各自单独串行执行，并天然构成批次屏障；
//   - 未知工具即时产出结构化失败结果，串行、不并发。
//
// 顺序不变量：结果槽位与 onEnd 回调都按模型给出的调用原序排列——事件流与
// 历史同序，顺序字节稳定是协议配对与缓存前缀稳定的前提。
package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/tools"
)

// execOutcome is the product of one call's execution, index-aligned with the
// input calls.
type execOutcome struct {
	Result  tools.ToolResult
	Elapsed time.Duration
	Unknown bool // the call named an unregistered tool; nothing really executed
}

// executeToolCalls executes calls in safety batches while preserving call
// order. onEnd is invoked in original call order: immediately for serial
// calls, and after a read batch joins, in ascending index order. The
// returned slice is len(calls) long, ordered by call position regardless of
// completion timing.
func executeToolCalls(ctx context.Context, calls []conversation.ToolUseBlock,
	reg *tools.Registry, onEnd func(index int, o execOutcome)) []execOutcome {

	outcomes := make([]execOutcome, len(calls))
	notify := func(i int) {
		if onEnd != nil {
			onEnd(i, outcomes[i])
		}
	}

	for i := 0; i < len(calls); {
		tool := reg.Get(calls[i].Name)
		switch {
		case tool == nil:
			// Unknown tool: instant structured failure, never concurrent.
			outcomes[i] = execOutcome{
				Result: tools.ToolResult{
					Output:  "unknown tool " + calls[i].Name + "; pick one of the tools offered",
					IsError: true,
				},
				Unknown: true,
			}
			notify(i)
			i++
		case tool.Category() != tools.CategoryRead:
			// Write / command: run alone as a barrier between read batches.
			outcomes[i] = runOne(ctx, tool, calls[i])
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
					outcomes[k] = runOne(ctx, reg.Get(calls[k].Name), calls[k])
				}(k)
			}
			wg.Wait()
			for k := i; k < j; k++ {
				notify(k)
			}
			i = j
		}
	}
	return outcomes
}

// runOne executes one registered tool call and times it. A panicking tool is
// folded into an error result — failures are values, not panics, per the Tool
// contract — so one broken tool can neither kill the process nor leave its
// batch-mates' outcomes unwritten.
func runOne(ctx context.Context, tool tools.Tool, call conversation.ToolUseBlock) (oc execOutcome) {
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			oc = execOutcome{
				Result: tools.ToolResult{
					Output:  fmt.Sprintf("tool %s panicked: %v", call.Name, r),
					IsError: true,
				},
				Elapsed: time.Since(start),
			}
		}
	}()
	return execOutcome{Result: tool.Execute(ctx, call.Args), Elapsed: time.Since(start)}
}
