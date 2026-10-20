package agent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/permissions"
)

var ErrPermissionNotPending = errors.New("agent: permission request is not pending")

type pendingPermission struct {
	id    string
	reply chan permissions.Response
}

type Turn struct {
	agent *Agent
	ctx   context.Context
	conv  *conversation.Manager

	eventsMu sync.Mutex
	started  bool
	stopped  bool

	permissionMu sync.Mutex
	askMu        sync.Mutex
	permissionID uint64
	pending      *pendingPermission
}

type roundResult struct {
	text      string
	thinking  []conversation.ThinkingBlock
	toolCalls []conversation.ToolUseBlock
	usage     llm.Usage
	err       error
}

// Events runs the turn once. A second call returns an empty sequence; the
// caller must cancel ctx when it stops before the terminal event.
func (t *Turn) Events() iter.Seq[TurnEvent] {
	return func(yield func(TurnEvent) bool) {
		t.eventsMu.Lock()
		if t.started {
			t.eventsMu.Unlock()
			return
		}
		t.started = true
		t.eventsMu.Unlock()
		t.run(yield)
	}
}

// AnswerPermission resolves the pending request named by id. A stale dialog
// gets ErrPermissionNotPending and must not affect a later request.
func (t *Turn) AnswerPermission(id string, response permissions.Response) error {
	t.permissionMu.Lock()
	pending := t.pending
	if pending == nil || pending.id != id {
		t.permissionMu.Unlock()
		return ErrPermissionNotPending
	}
	t.pending = nil
	t.permissionMu.Unlock()
	pending.reply <- response
	return nil
}

func (t *Turn) run(yield func(TurnEvent) bool) {
	send := func(event TurnEvent) bool {
		return t.emit(yield, event)
	}

	for round := 1; ; round++ {
		result := t.runRound(send)

		if t.ctx.Err() != nil {
			err := result.commitTo(t.conv)
			if err == nil {
				err = result.commitInterrupted(t.conv, "cancelled before execution")
			}
			if err == nil {
				t.publishUsage(result.usage, send)
			}
			send(TurnCompleted{Reason: StoppedByUser, ToolRounds: round - 1})
			return
		}
		if result.err != nil {
			send(TurnFailed{Err: result.err})
			return
		}
		if err := result.commitTo(t.conv); err != nil {
			send(TurnFailed{Err: err})
			return
		}
		if !t.publishUsage(result.usage, send) {
			return
		}
		if len(result.toolCalls) == 0 {
			send(TurnCompleted{Reason: StoppedByModel, ToolRounds: round - 1})
			return
		}
		if round > maxToolRounds {
			_ = result.commitInterrupted(t.conv, "iteration limit reached before this call could run")
			send(TurnCompleted{Reason: StoppedByLimit, ToolRounds: round - 1})
			return
		}
		if err := t.conv.AddToolResults(t.executeToolCalls(result.toolCalls, send)...); err != nil {
			send(TurnFailed{Err: err})
			return
		}
	}
}

func (t *Turn) emit(yield func(TurnEvent) bool, event TurnEvent) bool {
	t.eventsMu.Lock()
	defer t.eventsMu.Unlock()
	if t.stopped {
		return false
	}
	if !yield(event) {
		t.stopped = true
		return false
	}
	return true
}

func (t *Turn) runRound(send eventSink) roundResult {
	skipPrepare := false
	for attempt := 1; ; attempt++ {
		if t.agent.compactor != nil && !skipPrepare {
			t.agent.compactor.Prepare(t.ctx, t.agent.notifyCompaction(send))
		}
		skipPrepare = false
		if err := t.agent.injectReminders(t.conv); err != nil {
			return roundResult{err: err}
		}
		messages := conversation.EnsureToolPairing(t.conv.Messages())
		messages = t.agent.repairToolRefs(messages)
		result := t.stream(messages, send)
		if attempt == 1 && t.agent.compactor != nil && llm.IsContextTooLong(result.err) {
			if _, err := t.agent.compactor.ForceCompact(t.ctx, t.agent.notifyCompaction(send)); err == nil {
				skipPrepare = true
				continue
			}
		}
		return result
	}
}

func (t *Turn) stream(messages []conversation.Message, send eventSink) roundResult {
	events := t.agent.client.Stream(t.ctx, llm.Request{
		Messages: messages,
		Tools:    t.agent.registry.Schemas(),
	})

	var (
		result roundResult
		text   strings.Builder
	)
	finish := func() roundResult {
		result.text = text.String()
		return result
	}

	for event, err := range events {
		if err != nil {
			result.err = err
			return finish()
		}
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
			if !send(ThinkingDelta{Kind: mapThinkingKind(e.Kind), Text: e.Text}) {
				return finish()
			}
		case llm.ThinkingComplete:
			result.thinking = append(result.thinking, conversation.ThinkingBlock{
				Text:      e.Thinking,
				Signature: e.Signature,
				Content:   e.Content,
				Encrypted: e.Encrypted,
			})
			if e.Thinking != "" || e.Content != "" {
				if !send(ThinkingCompleted{Summary: e.Thinking, Content: e.Content}) {
					return finish()
				}
			}
		case llm.ToolCallComplete:
			result.toolCalls = append(result.toolCalls, conversation.ToolUseBlock{
				ID:   e.ToolID,
				Name: e.ToolName,
				Args: e.Arguments,
			})
			if !send(ToolStarted{ToolID: e.ToolID, ToolName: e.ToolName, Args: e.Arguments}) {
				return finish()
			}
		case llm.StreamEnd:
			result.usage = e.Usage
			return finish()
		}
	}
	return finish()
}

func (t *Turn) publishUsage(usage llm.Usage, send eventSink) bool {
	if usage == (llm.Usage{}) {
		return true
	}
	contextUsage := ContextUsage{}
	if t.agent.compactor != nil {
		t.agent.compactor.RecordUsage(usage)
		contextUsage = ContextUsage{
			UsedTokens:   t.agent.compactor.UsedTokens(),
			WindowTokens: t.agent.compactor.ContextWindow(),
			Level:        t.agent.compactor.Level(),
		}
	}
	return send(UsageUpdated{Usage: usage, Context: contextUsage})
}

func (t *Turn) askPermission(callID string, send eventSink, prompt permissions.Prompt) (allowed bool, note string) {
	t.askMu.Lock()
	defer t.askMu.Unlock()

	reply := make(chan permissions.Response, 1)
	id := callID
	if id == "" {
		t.permissionMu.Lock()
		t.permissionID++
		id = fmt.Sprintf("permission-%d", t.permissionID)
		t.permissionMu.Unlock()
	}

	t.permissionMu.Lock()
	t.pending = &pendingPermission{id: id, reply: reply}
	t.permissionMu.Unlock()

	if !send(PermissionRequested{ID: id, Prompt: prompt}) {
		t.clearPermission(id)
		return false, ""
	}

	var response permissions.Response
	select {
	case response = <-reply:
	case <-t.ctx.Done():
		t.clearPermission(id)
		return false, ""
	}
	if err := t.agent.checker.RecordGrant(prompt.Rules, response); err != nil {
		note = "this call ran, but the permanent rule could not be saved: " + err.Error()
	}
	return response.Allows(), note
}

func (t *Turn) clearPermission(id string) {
	t.permissionMu.Lock()
	defer t.permissionMu.Unlock()
	if t.pending != nil && t.pending.id == id {
		t.pending = nil
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

func mapThinkingKind(kind llm.ThinkingKind) ThinkingKind {
	switch kind {
	case llm.ThinkingSummary:
		return ThinkingSummary
	case llm.ThinkingContent:
		return ThinkingContent
	default:
		return ThinkingUnknown
	}
}
