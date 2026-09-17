package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/tools"
)

type callResult struct {
	Result  tools.ToolResult
	Elapsed time.Duration
}

func (a *Agent) executeToolCalls(ctx context.Context, calls []conversation.ToolUseBlock, send func(AgentEvent) bool, onDone func(index int, res callResult)) []callResult {
	results := make([]callResult, len(calls))
	notify := func(i int) {
		if onDone != nil {
			onDone(i, results[i])
		}
	}

	for i := 0; i < len(calls); {
		tool := a.registry.Get(calls[i].Name)
		switch {
		case tool == nil:
			results[i] = callResult{
				Result: tools.ToolResult{
					Output:  "unknown tool " + calls[i].Name + "; pick one of the tools offered",
					IsError: true,
				},
			}
			notify(i)
			i++
		case tool.Category() != tools.CategoryRead:
			results[i] = a.runTool(ctx, tool, calls[i], send)
			notify(i)
			i++
		default:
			j := i + 1
			for j < len(calls) {
				next := a.registry.Get(calls[j].Name)
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
					results[k] = a.runTool(ctx, a.registry.Get(calls[k].Name), calls[k], send)
					notify(k)
				}(k)
			}
			wg.Wait()
			i = j
		}
	}
	return results
}

func (a *Agent) runTool(ctx context.Context, tool tools.Tool, call conversation.ToolUseBlock, send func(AgentEvent) bool) (res callResult) {
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
	fail := func(reason string) callResult {
		return callResult{
			Result:  tools.ToolResult{Output: reason, IsError: true},
			Elapsed: time.Since(start),
		}
	}

	if a.checker != nil {
		switch decision := a.checker.Check(tool, call.Args); decision.Effect {
		case permissions.Deny:
			return fail(decision.Reason)
		case permissions.Ask:
			allowed, note := a.askPermission(ctx, send, tool, call)
			if !allowed {
				return fail("The user rejected this call (" + decision.Reason + ").")
			}
			// Waiting for the answer was the user's time, not the tool's.
			start = time.Now()
			result := tool.Execute(ctx, call.Args)
			if note != "" {
				result.Output = strings.TrimSpace(result.Output + "\n\n" + note)
			}
			return callResult{Result: result, Elapsed: time.Since(start)}
		}
	}
	return callResult{Result: tool.Execute(ctx, call.Args), Elapsed: time.Since(start)}
}

func (a *Agent) askPermission(ctx context.Context, send func(AgentEvent) bool, tool tools.Tool, call conversation.ToolUseBlock) (bool, string) {
	rules := a.checker.SuggestedRules(tool, call.Args)
	reply := make(chan PermissionResponse, 1)
	a.askMu.Lock()
	defer a.askMu.Unlock()

	if !send(PermissionRequestEvent{Request: PermissionRequest{
		ToolName: tool.Name(),
		Action:   permissions.ExtractContent(tool.Name(), call.Args),
		Rules:    rules,
		Response: reply,
	}}) {
		return false, "" // the turn ended before the question was asked
	}

	var response PermissionResponse
	select {
	case response = <-reply:
	case <-ctx.Done():
		return false, ""
	}

	switch response {
	case PermAllowSession:
		for _, rule := range rules {
			a.checker.AddSessionRule(rule)
		}
	case PermAllowPermanent:
		var failures []string
		for _, rule := range rules {
			if err := a.checker.AddPermanentRule(rule); err != nil {
				failures = append(failures, err.Error())
			}
		}
		if len(failures) > 0 {
			return true, "this call ran, but the permanent rule could not be saved: " +
				strings.Join(failures, "; ")
		}
	}
	return response != PermReject, ""
}
