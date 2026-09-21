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

// executeToolCalls runs the model's calls and returns one result block per call,
// in the order the model issued them — a provider pairs the replies with the
// calls by position. Consecutive calls to read-only tools run together; every
// other call runs on its own.
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

// runTool turns one call into a result: the permission ladder gets its say, the
// user gets asked when the ladder says so, and a panic is recovered into an
// ordinary failed result so the loop survives it.
func (a *Agent) runTool(ctx context.Context, call conversation.ToolUseBlock, send eventSink) (result tools.ToolResult, elapsed time.Duration) {
	tool := a.registry.Get(call.Name)
	if tool == nil {
		return tools.ToolResult{
			Output:  "unknown tool " + call.Name + "; pick one of the tools offered",
			IsError: true,
		}, 0
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
			result = tool.Execute(ctx, call.Args)
			if note != "" {
				result.Output = strings.TrimSpace(result.Output + "\n\n" + note)
			}
			return result, time.Since(start)
		}
	}
	return tool.Execute(ctx, call.Args), time.Since(start)
}

// askPermission puts the question to the UI and records what the answer grants.
// askMu serializes the dialogs: two read-only calls can reach this at once, and
// the UI shows one prompt at a time.
func (a *Agent) askPermission(ctx context.Context, send eventSink, tool tools.Tool, call conversation.ToolUseBlock) (allowed bool, note string) {
	request := a.checker.Question(tool, call.Args)
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
