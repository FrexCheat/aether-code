package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/tools"
)

func (t *Turn) executeToolCalls(calls []conversation.ToolUseBlock, send eventSink) []conversation.ToolResultBlock {
	results := make([]conversation.ToolResultBlock, len(calls))
	for first := 0; first < len(calls); {
		last := first + 1
		if runsInParallel(t.agent.registry.Get(calls[first].Name)) {
			for last < len(calls) && runsInParallel(t.agent.registry.Get(calls[last].Name)) {
				last++
			}
		}
		if last-first == 1 {
			results[first] = t.runCall(calls[first], send)
		} else {
			var wg sync.WaitGroup
			for i := first; i < last; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					results[i] = t.runCall(calls[i], send)
				}(i)
			}
			wg.Wait()
		}
		first = last
	}
	return results
}

func runsInParallel(tool tools.Tool) bool {
	return tool != nil && tool.Category() == tools.CategoryRead
}

func (t *Turn) runCall(call conversation.ToolUseBlock, send eventSink) conversation.ToolResultBlock {
	result, elapsed := t.runTool(call, send)
	send(ToolFinished{ToolID: call.ID, ToolName: call.Name, Result: result, Elapsed: elapsed})
	return conversation.ToolResultBlock{
		ToolUseID: call.ID,
		Content:   result.Output,
		IsError:   result.IsError,
		ToolRefs:  result.ToolRefs,
	}
}

func (t *Turn) runTool(call conversation.ToolUseBlock, send eventSink) (result tools.ToolResult, elapsed time.Duration) {
	tool := t.agent.registry.Get(call.Name)
	if tool == nil {
		return tools.ToolResult{
			Output:  "unknown tool " + call.Name + "; pick one of the tools offered",
			IsError: true,
		}, 0
	}
	args := call.Args
	if dispatcher, ok := tool.(tools.Dispatcher); ok {
		target, targetArgs, err := dispatcher.Target(args)
		if err != nil {
			return tools.ToolResult{Output: err.Error(), IsError: true}, 0
		}
		tool, args = target, targetArgs
	}

	start := time.Now()
	defer func() {
		if recovered := recover(); recovered != nil {
			result = tools.ToolResult{
				Output:  fmt.Sprintf("tool %s panicked: %v", call.Name, recovered),
				IsError: true,
			}
			elapsed = time.Since(start)
		}
	}()
	fail := func(reason string) (tools.ToolResult, time.Duration) {
		return tools.ToolResult{Output: reason, IsError: true}, time.Since(start)
	}

	if t.agent.checker != nil {
		switch decision := t.agent.checker.Check(tool, args); decision.Effect {
		case permissions.Deny:
			return fail(decision.Reason)
		case permissions.Ask:
			prompt := t.agent.checker.Question(tool, args)
			allowed, note := t.askPermission(call.ID, send, prompt)
			if !allowed {
				return fail("The user rejected this call (" + decision.Reason + ").")
			}
			start = time.Now()
			result = tool.Execute(t.ctx, args)
			if note != "" {
				result.Output = strings.TrimSpace(result.Output + "\n\n" + note)
			}
			return result, time.Since(start)
		}
	}
	return tool.Execute(t.ctx, args), time.Since(start)
}
