// Package agent orchestrates a full agent loop: it assembles the LLM request
// (history + tool list), consumes the provider stream, executes the model's
// tool calls, and feeds the results back until the model stops asking.
//
// A Turn is one iteration — one request plus its tool executions; the Loop is
// the whole sequence of turns. RunLoop drives one Loop per user message and
// reports progress through the event stream defined in events.go.
package agent

import (
	"context"
	"strings"
	"sync"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/prompt"
	"github.com/FrexCheat/aether-code/internal/tools"
)

type Mode string

const (
	ModeNormal Mode = "normal"
	ModePlan   Mode = "plan"
)

const defaultMaxTurns = 50

type Orchestrator struct {
	client   llm.Client
	registry *tools.Registry
	conv     *conversation.Manager
	system   string
	thinking bool
	maxTurns int
	mu       sync.RWMutex
	mode     Mode
}

func New(client llm.Client, registry *tools.Registry, conv *conversation.Manager, system string, thinking bool, maxTurns int) *Orchestrator {
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}
	return &Orchestrator{
		client:   client,
		registry: registry,
		conv:     conv,
		system:   system,
		thinking: thinking,
		maxTurns: maxTurns,
		mode:     ModeNormal,
	}
}

func (o *Orchestrator) Mode() Mode {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.mode
}

func (o *Orchestrator) SetMode(m Mode) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.mode = m
}

func (o *Orchestrator) RunLoop(ctx context.Context, userText string) <-chan AgentEvent {
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

		o.conv.AddUserMessage(userText)

		var turns int           // executed tool rounds
		var usage llm.UsageInfo // accumulated over completed rounds

		for {
			msgs := conversation.EnsureToolPairing(o.conv.GetMessages())
			mode := o.Mode()

			system := o.system
			schemas := o.registry.Schemas(nil)
			if mode == ModePlan {
				system += "\n\n" + prompt.PlanSegment
				schemas = o.registry.Schemas(func(c tools.Category) bool { return c == tools.CategoryRead })
			}

			events := o.client.Stream(ctx, llm.Request{
				System:   system,
				Messages: msgs,
				Tools:    schemas,
				Thinking: o.thinking,
			})

			var (
				text      strings.Builder
				thinking  []conversation.ThinkingBlock
				toolUses  []conversation.ToolUseBlock
				end       llm.StreamEnd
				streamErr error
				cancelled bool
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
					thinking = append(thinking, conversation.ThinkingBlock{
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
					end = e
					break consume
				case llm.StreamError:
					streamErr = e.Err
					break consume
				}
			}

			usage.InputTokens += end.Usage.InputTokens
			usage.OutputTokens += end.Usage.OutputTokens
			usage.CacheReadTokens += end.Usage.CacheReadTokens
			usage.CacheCreationTokens += end.Usage.CacheCreationTokens

			if cancelled || ctx.Err() != nil {
				o.commitAssistant(text.String(), thinking, toolUses)
				o.commitSyntheticResults(toolUses, "cancelled before execution")
				terminal(LoopComplete{Reason: StoppedByUser, TotalTurns: turns, Usage: usage})
				return
			}
			if streamErr != nil {
				terminal(LoopError{Err: streamErr})
				return
			}

			o.commitAssistant(text.String(), thinking, toolUses)

			if len(toolUses) == 0 {
				terminal(LoopComplete{Reason: StoppedByModel, TotalTurns: turns, Usage: usage})
				return
			}

			if turns >= o.maxTurns {
				o.commitSyntheticResults(toolUses, "iteration limit reached before this call could run")
				terminal(LoopComplete{Reason: StoppedByLimit, TotalTurns: turns, Usage: usage})
				return
			}

			callResults := executeToolCalls(ctx, toolUses, o.registry, func(i int, res callResult) {
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
			o.conv.AddToolResults(resultBlocks...)
			turns++

			send(TurnComplete{Turn: turns})
			if ctx.Err() != nil {
				terminal(LoopComplete{Reason: StoppedByUser, TotalTurns: turns, Usage: usage})
				return
			}
		}
	}()
	return ch
}

func (o *Orchestrator) commitAssistant(text string, thinking []conversation.ThinkingBlock, toolUses []conversation.ToolUseBlock) {
	if text == "" && len(thinking) == 0 && len(toolUses) == 0 {
		return
	}
	o.conv.AddAssistantFull(text, thinking, toolUses)
}

func (o *Orchestrator) commitSyntheticResults(toolUses []conversation.ToolUseBlock, reason string) {
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
	o.conv.AddToolResults(results...)
}
