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

func (a *Agent) executeToolCalls(ctx context.Context, calls []conversation.ToolUseBlock, send eventSink) []conversation.ToolResultBlock {
	results := make([]conversation.ToolResultBlock, len(calls))
	for first := 0; first < len(calls); {
		last := first + 1
		if runsInParallel(a.registry.Get(calls[first].Name)) {
			for last < len(calls) && runsInParallel(a.registry.Get(calls[last].Name)) {
				last++
			}
		}
		if last-first == 1 {
			results[first] = a.runCall(ctx, calls[first], send)
		} else {
			// A cancelled turn does not stop the rest of the batch: each call
			// runs to its own end against the dead context, and the results are
			// what the conversation records.
			var wg sync.WaitGroup
			for i := first; i < last; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					results[i] = a.runCall(ctx, calls[i], send)
				}(i)
			}
			wg.Wait()
		}
		first = last
	}
	return results
}

// runsInParallel reports whether calls to a tool may run alongside their
// neighbours: only tools the registry knows, and only read-only ones.
func runsInParallel(tool tools.Tool) bool {
	return tool != nil && tool.Category() == tools.CategoryRead
}

// runCall executes one call and reports its end to the UI. The event carries the
// whole result, diff included, because the UI renders edits from it.
func (a *Agent) runCall(ctx context.Context, call conversation.ToolUseBlock, send eventSink) conversation.ToolResultBlock {
	result, elapsed := a.runTool(ctx, call, send)
	send(ToolCallEnd{ToolID: call.ID, ToolName: call.Name, Result: result, Elapsed: elapsed})
	return conversation.ToolResultBlock{
		ToolUseID: call.ID,
		Content:   result.Output,
		IsError:   result.IsError,
		ToolRefs:  result.ToolRefs,
	}
}

func (a *Agent) runTool(ctx context.Context, call conversation.ToolUseBlock, send eventSink) (result tools.ToolResult, elapsed time.Duration) {
	tool := a.registry.Get(call.Name)
	if tool == nil {
		return tools.ToolResult{
			Output:  "unknown tool " + call.Name + "; pick one of the tools offered",
			IsError: true,
		}, 0
	}
	// A dispatcher tool runs another registered tool picked from its
	// arguments. Resolving before the ladder makes the target the permission
	// subject: rules, the dialog and the grant anchor on the target tool's
	// name, however the model addressed the call.
	args := call.Args
	if d, ok := tool.(tools.Dispatcher); ok {
		target, targetArgs, err := d.Target(args)
		if err != nil {
			return tools.ToolResult{Output: err.Error(), IsError: true}, 0
		}
		tool, args = target, targetArgs
	}

	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			result = tools.ToolResult{
				Output:  fmt.Sprintf("tool %s panicked: %v", call.Name, r),
				IsError: true,
			}
			elapsed = time.Since(start)
		}
	}()
	fail := func(reason string) (tools.ToolResult, time.Duration) {
		return tools.ToolResult{Output: reason, IsError: true}, time.Since(start)
	}

	if a.checker != nil {
		switch decision := a.checker.Check(tool, args); decision.Effect {
		case permissions.Deny:
			return fail(decision.Reason)
		case permissions.Ask:
			allowed, note := a.askPermission(ctx, send, tool, args)
			if !allowed {
				return fail("The user rejected this call (" + decision.Reason + ").")
			}
			// Waiting for the answer was the user's time, not the tool's.
			start = time.Now()
			result = tool.Execute(ctx, args)
			if note != "" {
				result.Output = strings.TrimSpace(result.Output + "\n\n" + note)
			}
			return result, time.Since(start)
		}
	}
	return tool.Execute(ctx, args), time.Since(start)
}

func (a *Agent) askPermission(ctx context.Context, send eventSink, tool tools.Tool, args map[string]any) (allowed bool, note string) {
	request := a.checker.Question(tool, args)
	reply := make(chan permissions.Response, 1)
	request.Reply = reply

	a.askMu.Lock()
	defer a.askMu.Unlock()

	if !send(PermissionRequestEvent{Request: request}) {
		return false, "" // the turn ended before the question was asked
	}

	var response permissions.Response
	select {
	case response = <-reply:
	case <-ctx.Done():
		return false, ""
	}

	if err := a.checker.RecordGrant(request.Rules, response); err != nil {
		// The user's answer stands; only the written record of it failed.
		note = "this call ran, but the permanent rule could not be saved: " + err.Error()
	}
	return response.Allows(), note
}
