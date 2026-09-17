// Package agent runs the agent loop: it assembles the LLM request
// (history + tool list), consumes the provider stream, executes the model's
// tool calls, and feeds the results back until the model stops asking.
//
// A Turn is one iteration — one request plus its tool executions; the Loop is
// the whole sequence of turns. Run drives one Loop over the conversation and
// reports progress through the event stream defined in events.go.
package agent

import (
	"context"
	"strings"
	"sync"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/tools"
)

// defaultMaxTurns caps the tool rounds one Run may execute before the loop
// stops with StoppedByLimit.
const defaultMaxTurns = 50

// Agent drives the agent loop over a conversation. It holds no session state:
// the conversation belongs to the caller and is passed to Run. The
// conversation and the checker belong to the caller too, so a mode switch made
// in the UI reaches a loop that is already running.
type Agent struct {
	client   llm.Client
	registry *tools.Registry
	checker  *permissions.Checker // nil disables permission checks entirely

	askMu sync.Mutex // one confirmation dialog at a time
}

// New builds an agent. A nil checker means tool calls run unchecked.
func New(client llm.Client, registry *tools.Registry, checker *permissions.Checker) *Agent {
	return &Agent{client: client, registry: registry, checker: checker}
}

// Checker returns the shared permission checker, or nil when checks are off.
// The UI reads and reconfigures the session's mode through this instance, so
// what it shows and what the loop enforces are the same thing.
func (a *Agent) Checker() *permissions.Checker { return a.checker }

// Run drives conv until the model stops requesting tools. The caller seeds
// conv with the user message beforehand; Run owns the loop from that point on
// and reports progress through the returned event channel.
func (a *Agent) Run(ctx context.Context, conv *conversation.Manager) <-chan AgentEvent {
	ch := make(chan AgentEvent, 64)
	go func() {
		defer close(ch)
		send := func(ev AgentEvent) bool {
			select {
			case ch <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}
		terminal := func(ev AgentEvent) {
			ch <- ev
		}

		var totalUsage llm.UsageInfo // accumulated over completed rounds

		// turn is the current loop iteration, 1-based; turn-1 equals the
		// number of tool rounds executed so far.
		for turn := 1; ; turn++ {
			msgs := conversation.EnsureToolPairing(conv.GetMessages())
			events := a.client.Stream(ctx, llm.Request{
				Messages: msgs,
				Tools:    a.registry.Schemas(),
			})

			var (
				text           strings.Builder
				thinkingBlocks []conversation.ThinkingBlock
				toolUses       []conversation.ToolUseBlock
				streamEnd      llm.StreamEnd
				streamErr      error
				cancelled      bool
			)
		consume:
			for ev := range events {
				switch e := ev.(type) {
				case llm.TextDelta:
					text.WriteString(e.Text)
					if !send(TextDelta{Text: e.Text}) {
						cancelled = true
						break consume
					}
				case llm.TextComplete:
					if !send(TextComplete{}) {
						cancelled = true
						break consume
					}
				case llm.ThinkingDelta:
					if !send(ThinkingDelta{Text: e.Text}) {
						cancelled = true
						break consume
					}
				case llm.ThinkingComplete:
					thinkingBlocks = append(thinkingBlocks, conversation.ThinkingBlock{
						Text:      e.Thinking,
						Signature: e.Signature,
					})
					if !send(ThinkingComplete{Thinking: e.Thinking, Signature: e.Signature}) {
						cancelled = true
						break consume
					}
				case llm.ToolCallComplete:
					toolUses = append(toolUses, conversation.ToolUseBlock{
						ID:   e.ToolID,
						Name: e.ToolName,
						Args: e.Arguments,
					})
					if !send(ToolCallBegin{ToolID: e.ToolID, ToolName: e.ToolName, Args: e.Arguments}) {
						cancelled = true
						break consume
					}
				case llm.StreamEnd:
					streamEnd = e
					break consume
				case llm.StreamError:
					streamErr = e.Err
					break consume
				}
			}

			totalUsage.InputTokens += streamEnd.Usage.InputTokens
			totalUsage.OutputTokens += streamEnd.Usage.OutputTokens
			totalUsage.CacheReadTokens += streamEnd.Usage.CacheReadTokens
			totalUsage.CacheCreationTokens += streamEnd.Usage.CacheCreationTokens

			if cancelled || ctx.Err() != nil {
				commitAssistant(conv, text.String(), thinkingBlocks, toolUses)
				commitSyntheticResults(conv, toolUses, "cancelled before execution")
				terminal(LoopComplete{Reason: StoppedByUser, TotalTurns: turn - 1, Usage: totalUsage})
				return
			}
			if streamErr != nil {
				terminal(LoopError{Err: streamErr})
				return
			}

			commitAssistant(conv, text.String(), thinkingBlocks, toolUses)

			if len(toolUses) == 0 {
				terminal(LoopComplete{Reason: StoppedByModel, TotalTurns: turn - 1, Usage: totalUsage})
				return
			}

			if turn > defaultMaxTurns {
				commitSyntheticResults(conv, toolUses, "iteration limit reached before this call could run")
				terminal(LoopComplete{Reason: StoppedByLimit, TotalTurns: turn - 1, Usage: totalUsage})
				return
			}

			callResults := a.executeToolCalls(ctx, toolUses, send, func(i int, res callResult) {
				send(ToolCallEnd{
					ToolID:   toolUses[i].ID,
					ToolName: toolUses[i].Name,
					Result:   res.Result,
					Elapsed:  res.Elapsed,
				})
			})

			resultBlocks := make([]conversation.ToolResultBlock, 0, len(callResults))
			for i, res := range callResults {
				resultBlocks = append(resultBlocks, conversation.ToolResultBlock{
					ToolUseID: toolUses[i].ID,
					Content:   res.Result.Output,
					IsError:   res.Result.IsError,
				})
			}
			conv.AddToolResults(resultBlocks...)
		}
	}()
	return ch
}

func commitAssistant(conv *conversation.Manager, text string, thinking []conversation.ThinkingBlock, toolUses []conversation.ToolUseBlock) {
	if text == "" && len(thinking) == 0 && len(toolUses) == 0 {
		return
	}
	conv.AddAssistantFull(text, thinking, toolUses)
}

func commitSyntheticResults(conv *conversation.Manager, toolUses []conversation.ToolUseBlock, reason string) {
	if len(toolUses) == 0 {
		return
	}
	results := make([]conversation.ToolResultBlock, 0, len(toolUses))
	for _, tu := range toolUses {
		results = append(results, conversation.ToolResultBlock{
			ToolUseID: tu.ID,
			Content:   reason,
			IsError:   true,
		})
	}
	conv.AddToolResults(results...)
}
