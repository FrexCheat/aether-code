package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/FrexCheat/aether-code/internal/compaction"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/tools"
)

const maxToolRounds = 50

const deferredReminderTemplate = "以下 MCP 工具处于延迟加载状态，当前上下文中只有名字、没有参数定义：\n" +
	"%s\n\n" +
	"需要使用时，先用 ToolSearch 工具检索（支持 select:名字,名字 精确获取，或关键词搜索）获取完整定义；" +
	"拿到定义后按其说明调用：通常按原名直接调用，定义以文本返回时经 McpCall（server/tool/arguments）调用。"

// deferredMarker is the reminder's stable first line; the presence probe keys
// on it so the text is injected once per session and re-injected after
// compaction drops it.
const deferredMarker = "以下 MCP 工具处于延迟加载状态"

// Compactor is the loop's view of the compaction layer.
type Compactor interface {
	Prepare(ctx context.Context, notify func(compaction.Progress))
	ForceCompact(ctx context.Context, notify func(compaction.Progress)) (compaction.Result, error)
	RecordUsage(u llm.Usage)
}

var _ Compactor = (*compaction.Manager)(nil)

type Agent struct {
	client        llm.Client
	registry      *tools.Registry
	checker       *permissions.Checker // nil disables permission checks entirely
	deferredNames func() []string      // nil: every schema is already on the wire
	compactor     Compactor            // nil disables context compaction
	askMu         sync.Mutex           // one confirmation dialog at a time
}

type roundResult struct {
	text      string
	thinking  []conversation.ThinkingBlock
	toolCalls []conversation.ToolUseBlock
	usage     llm.Usage
	err       error
}

func New(client llm.Client, registry *tools.Registry, checker *permissions.Checker, deferredNames func() []string, compactor Compactor) *Agent {
	return &Agent{
		client:        client,
		registry:      registry,
		checker:       checker,
		deferredNames: deferredNames,
		compactor:     compactor,
	}
}

func (a *Agent) Checker() *permissions.Checker { return a.checker }

func (a *Agent) Registry() *tools.Registry { return a.registry }

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

		for round := 1; ; round++ {
			result := a.runRound(ctx, conv, send)

			if ctx.Err() != nil {
				// The stop reason is already decided by the context: a refused
				// commit keeps StoppedByUser rather than adding a second
				// terminal event (the TUI stops folding after the first), and
				// the session surfaces the write failure once on its own.
				err := result.commitTo(conv)
				if err == nil {
					err = result.commitInterrupted(conv, "cancelled before execution")
				}
				if err == nil && result.err == nil && a.compactor != nil && result.usage != (llm.Usage{}) {
					a.compactor.RecordUsage(result.usage)
				}
				terminal(LoopComplete{Reason: StoppedByUser, ToolRounds: round - 1, Usage: result.usage})
				return
			}
			if result.err != nil {
				terminal(LoopError{Err: result.err})
				return
			}

			if err := result.commitTo(conv); err != nil {
				terminal(LoopError{Err: err})
				return
			}
			if a.compactor != nil && result.usage != (llm.Usage{}) {
				a.compactor.RecordUsage(result.usage)
			}

			if len(result.toolCalls) == 0 {
				terminal(LoopComplete{Reason: StoppedByModel, ToolRounds: round - 1, Usage: result.usage})
				return
			}
			if round > maxToolRounds {
				// Like the cancel path, the limit already ended the turn, so a
				// refused commit keeps StoppedByLimit.
				_ = result.commitInterrupted(conv, "iteration limit reached before this call could run")
				terminal(LoopComplete{Reason: StoppedByLimit, ToolRounds: round - 1, Usage: result.usage})
				return
			}

			if err := conv.AddToolResults(a.executeToolCalls(ctx, result.toolCalls, send)...); err != nil {
				terminal(LoopError{Err: err})
				return
			}
		}
	}()
	return ch
}

// runRound is one request attempt, plus the reactive fallback: a
// context-too-long rejection force-compacts and retries the round once, the
// provider having refused before any content block arrived.
func (a *Agent) runRound(ctx context.Context, conv *conversation.Manager, send eventSink) roundResult {
	skipPrepare := false
	for attempt := 1; ; attempt++ {
		if a.compactor != nil && !skipPrepare {
			// Prepare rewrites the stored history, so it must run before the
			// request copy is taken.
			a.compactor.Prepare(ctx, a.notifyCompaction(send))
		}
		skipPrepare = false
		// The reminders ride the stored history: one stable copy per session,
		// put back whenever compaction dropped it. They never reach the
		// archive (the session's journal skips reminders), so a resume
		// rebuilds them from the current world instead of replaying them.
		if err := a.injectReminders(conv); err != nil {
			return roundResult{err: err}
		}
		messages := conversation.EnsureToolPairing(conv.Messages())
		messages = a.repairToolRefs(messages)
		result := a.stream(ctx, messages, send)
		if attempt == 1 && a.compactor != nil && llm.IsContextTooLong(result.err) {
			if _, err := a.compactor.ForceCompact(ctx, a.notifyCompaction(send)); err == nil {
				// The force pass already ran Layer 1, and Layer 2 would
				// re-trigger at the still-elevated estimate whenever the kept
				// tail itself sits above the line: skip exactly one Prepare.
				skipPrepare = true
				continue
			}
		}
		return result
	}
}

// injectReminders appends the deferred-tools reminder unless its marker is
// already in the history. The AETHER.md chain is not here: it is a system
// prompt section, outside the message history, so compaction cannot drop it.
func (a *Agent) injectReminders(conv *conversation.Manager) error {
	if a.deferredNames == nil {
		return nil
	}
	names := a.deferredNames()
	if len(names) == 0 {
		return nil
	}
	_, err := conversation.EnsureReminder(conv, deferredMarker, deferredReminder(names))
	return err
}

// repairToolRefs drops tool_reference names the registry no longer offers on
// this request's copy. After an MCP change a replayed ToolSearch result could
// otherwise reference a tool that is not on the wire — which strict Anthropic
// endpoints reject, and the message sits in the kept tail where compaction
// never reaches. The stored history keeps the original references.
func (a *Agent) repairToolRefs(messages []conversation.Message) []conversation.Message {
	schemas := a.registry.Schemas()
	known := make(map[string]struct{}, len(schemas))
	for _, s := range schemas {
		known[s.Name] = struct{}{}
	}
	for i := range messages {
		for j := range messages[i].ToolResults {
			refs := messages[i].ToolResults[j].ToolRefs
			if len(refs) == 0 {
				continue
			}
			kept := make([]string, 0, len(refs))
			for _, name := range refs {
				if _, ok := known[name]; ok {
					kept = append(kept, name)
				}
			}
			if len(kept) != len(refs) {
				messages[i].ToolResults[j].ToolRefs = kept
			}
		}
	}
	return messages
}

func (a *Agent) stream(ctx context.Context, messages []conversation.Message, send eventSink) roundResult {
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

// notifyCompaction forwards compaction progress to the UI; a send on a
// cancelled turn just drops it.
func (a *Agent) notifyCompaction(send eventSink) func(compaction.Progress) {
	return func(p compaction.Progress) {
		send(CompactionProgress{Progress: p})
	}
}

func (r roundResult) commitTo(conv *conversation.Manager) error {
	if r.text == "" && len(r.thinking) == 0 && len(r.toolCalls) == 0 {
		return nil
	}
	return conv.AddAssistantMessage(r.text, r.thinking, r.toolCalls)
}

func (r roundResult) commitInterrupted(conv *conversation.Manager, reason string) error {
	if len(r.toolCalls) == 0 {
		return nil
	}
	results := make([]conversation.ToolResultBlock, 0, len(r.toolCalls))
	for _, call := range r.toolCalls {
		results = append(results, conversation.ToolResultBlock{
			ToolUseID: call.ID,
			Content:   reason,
			IsError:   true,
		})
	}
	return conv.AddToolResults(results...)
}

func deferredReminder(names []string) string {
	return fmt.Sprintf(deferredReminderTemplate, strings.Join(names, "\n"))
}
