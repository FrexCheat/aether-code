package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/tools"
)

const maxToolRounds = 50

// deferredReminderTemplate is model-facing Chinese, like the prompt sections.
// One static shape serves both deferred modes: which call path applies is
// whatever the ToolSearch result says about the tool it surfaced.
const deferredReminderTemplate = "以下 MCP 工具处于延迟加载状态，当前上下文中只有名字、没有参数定义：\n" +
	"%s\n\n" +
	"需要使用时，先用 ToolSearch 工具检索（支持 select:名字,名字 精确获取，或关键词搜索）获取完整定义；" +
	"拿到定义后按其说明调用：通常按原名直接调用，定义以文本返回时经 McpCall（server/tool/arguments）调用。"

type Agent struct {
	client        llm.Client
	registry      *tools.Registry
	checker       *permissions.Checker // nil disables permission checks entirely
	deferredNames func() []string      // nil: every schema is already on the wire
	askMu         sync.Mutex           // one confirmation dialog at a time
}

// roundResult is what one request to the provider produced: the assistant
// message being assembled, the tool calls it asks for, and how it ended.
type roundResult struct {
	text      string
	thinking  []conversation.ThinkingBlock
	toolCalls []conversation.ToolUseBlock
	usage     llm.Usage
	err       error
}

// New builds an agent. A nil checker means tool calls run unchecked; a nil
// deferredNames means no tool is deferred and the reminder stays out of every
// request.
func New(client llm.Client, registry *tools.Registry, checker *permissions.Checker, deferredNames func() []string) *Agent {
	return &Agent{client: client, registry: registry, checker: checker, deferredNames: deferredNames}
}

func (a *Agent) Checker() *permissions.Checker { return a.checker }

func (a *Agent) Run(ctx context.Context, conv *conversation.Manager) <-chan Event {
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		send := func(ev Event) bool {
			select {
			case ch <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}
		// Deliberately not ctx-aware: the terminal event is what ends the turn
		// in the UI, and on the cancelled path the context is already done.
		terminal := func(ev Event) {
			ch <- ev
		}

		var totalUsage llm.Usage // accumulated over completed rounds
		// round is the current iteration, 1-based: round-1 is the number of
		// tool rounds executed so far.
		for round := 1; ; round++ {
			result := a.runRound(ctx, conv, send)
			totalUsage.InputTokens += result.usage.InputTokens
			totalUsage.OutputTokens += result.usage.OutputTokens
			totalUsage.CacheReadTokens += result.usage.CacheReadTokens
			totalUsage.CacheCreationTokens += result.usage.CacheCreationTokens

			// The context is the only source of truth for cancellation, and it
			// is checked before the stream error: a round can end cleanly after
			// the context died (a cancel during the permission dialog or during
			// tool execution), and that is a cancelled turn, not a failure.
			if ctx.Err() != nil {
				result.commitTo(conv)
				result.commitInterrupted(conv, "cancelled before execution")
				terminal(LoopComplete{Reason: StoppedByUser, ToolRounds: round - 1, Usage: totalUsage})
				return
			}
			if result.err != nil {
				terminal(LoopError{Err: result.err})
				return
			}

			result.commitTo(conv)

			if len(result.toolCalls) == 0 {
				terminal(LoopComplete{Reason: StoppedByModel, ToolRounds: round - 1, Usage: totalUsage})
				return
			}
			if round > maxToolRounds {
				result.commitInterrupted(conv, "iteration limit reached before this call could run")
				terminal(LoopComplete{Reason: StoppedByLimit, ToolRounds: round - 1, Usage: totalUsage})
				return
			}

			conv.AddToolResults(a.executeToolCalls(ctx, result.toolCalls, send)...)
		}
	}()
	return ch
}

// runRound performs one request: it assembles the history, consumes the
// provider's stream into a roundResult, and forwards every event to the UI. The
// text is read off the builder on the way out, so an interrupted round still
// reports what the model managed to say.
func (a *Agent) runRound(ctx context.Context, conv *conversation.Manager, send eventSink) roundResult {
	messages := conversation.EnsureToolPairing(conv.Messages())
	// Deferred tools are announced on the request copy, never stored: the
	// reminder rides every round without growing the history.
	if a.deferredNames != nil {
		if names := a.deferredNames(); len(names) > 0 {
			messages = append(messages, conversation.NewReminderMessage(deferredReminder(names)))
		}
	}
	events := a.client.Stream(ctx, llm.Request{
		Messages: messages,
		Tools:    a.registry.Schemas(),
	})

	var (
		result roundResult
		text   strings.Builder
	)
	finish := func() roundResult {
		result.text = text.String()
		return result
	}

	for event := range events {
		switch e := event.(type) {
		case llm.TextDelta:
			text.WriteString(e.Text)
			if !send(TextDelta{Text: e.Text}) {
				return finish()
			}
		case llm.TextComplete:
			if !send(TextComplete{}) {
				return finish()
			}
		case llm.ThinkingDelta:
			if !send(ThinkingDelta{Text: e.Text}) {
				return finish()
			}
		case llm.ThinkingComplete:
			result.thinking = append(result.thinking, conversation.ThinkingBlock{
				Text:      e.Thinking,
				Signature: e.Signature,
			})
			if !send(ThinkingComplete{Thinking: e.Thinking, Signature: e.Signature}) {
				return finish()
			}
		case llm.ToolCallComplete:
			result.toolCalls = append(result.toolCalls, conversation.ToolUseBlock{
				ID:   e.ToolID,
				Name: e.ToolName,
				Args: e.Arguments,
			})
			if !send(ToolCallBegin{ToolID: e.ToolID, ToolName: e.ToolName, Args: e.Arguments}) {
				return finish()
			}
		case llm.StreamEnd:
			result.usage = e.Usage
			return finish()
		case llm.StreamError:
			result.err = e.Err
			return finish()
		}
	}
	return finish()
}

// commitTo appends the round's assistant message to the conversation. A round
// that produced nothing before failing adds no message at all.
func (r roundResult) commitTo(conv *conversation.Manager) {
	if r.text == "" && len(r.thinking) == 0 && len(r.toolCalls) == 0 {
		return
	}
	conv.AddAssistantMessage(r.text, r.thinking, r.toolCalls)
}

// commitInterrupted answers every tool call of the round with a failed result,
// so the history stays pair-complete even though nothing will ever run them.
func (r roundResult) commitInterrupted(conv *conversation.Manager, reason string) {
	if len(r.toolCalls) == 0 {
		return
	}
	results := make([]conversation.ToolResultBlock, 0, len(r.toolCalls))
	for _, call := range r.toolCalls {
		results = append(results, conversation.ToolResultBlock{
			ToolUseID: call.ID,
			Content:   reason,
			IsError:   true,
		})
	}
	conv.AddToolResults(results...)
}

func deferredReminder(names []string) string {
	return fmt.Sprintf(deferredReminderTemplate, strings.Join(names, "\n"))
}
