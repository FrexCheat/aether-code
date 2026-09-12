// Package agent orchestrates one user turn end to end: it assembles the LLM
// request (history + tool list), consumes the provider stream, and when the
// model stops to call tools, executes them and feeds structured results back
// into the conversation.
//
// This phase deliberately stops there — one tool round per user turn, no
// automatic re-request. The loop closes in a later phase; everything the
// orchestrator does here is already loop-shaped, so that phase only adds
// "after the results land, call Stream again".
package agent

import (
	"context"
	"strings"
	"time"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/tools"
)

// AgentEvent is the sealed event stream RunTurn feeds to the UI. Text and
// thinking events forward the provider stream; tool events bracket each
// execution; TurnEnd/TurnError are terminal and close the channel.
type AgentEvent interface{ agentEvent() }

type TextDelta struct{ Text string }

type ThinkingDelta struct{ Text string }

type ThinkingComplete struct {
	Text      string
	Signature string
}

// ToolCallBegin announces an execution about to start; Args is the parsed
// argument map the UI summarizes into a title.
type ToolCallBegin struct {
	ToolID   string
	ToolName string
	Args     map[string]any
}

// ToolCallEnd reports one execution's outcome and its wall time.
type ToolCallEnd struct {
	ToolID   string
	ToolName string
	Result   tools.ToolResult
	Elapsed  time.Duration
}

type TurnEnd struct {
	StopReason string
	Usage      llm.UsageInfo
}

type TurnError struct{ Err error }

func (TextDelta) agentEvent()        {}
func (ThinkingDelta) agentEvent()    {}
func (ThinkingComplete) agentEvent() {}
func (ToolCallBegin) agentEvent()    {}
func (ToolCallEnd) agentEvent()      {}
func (TurnEnd) agentEvent()          {}
func (TurnError) agentEvent()        {}

// Orchestrator wires the pieces one session talks to. The conversation
// manager is injected, not owned: the TUI keeps its handle on history.
type Orchestrator struct {
	client   llm.Client
	registry *tools.Registry
	conv     *conversation.Manager
	system   string
	thinking bool
	protocol string // schema flavor passed to the registry, e.g. "anthropic"/"openai"
}

func New(client llm.Client, registry *tools.Registry, conv *conversation.Manager,
	system string, thinking bool, protocol string) *Orchestrator {
	return &Orchestrator{
		client:   client,
		registry: registry,
		conv:     conv,
		system:   system,
		thinking: thinking,
		protocol: protocol,
	}
}

// RunTurn runs one user turn: user message → stream → optional single tool
// round → terminal event. Events are delivered on the returned channel, whose
// closure marks the end of the turn. Tool executions run synchronously inside
// the turn goroutine; cancellation follows ctx (the stream observes it, tool
// timeouts come from each tool).
func (o *Orchestrator) RunTurn(ctx context.Context, userText string) <-chan AgentEvent {
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

		o.conv.AddUserMessage(userText)
		events := o.client.Stream(ctx, llm.Request{
			System:   o.system,
			Messages: o.conv.GetMessages(),
			Tools:    o.registry.GetAllSchemas(o.protocol),
			Thinking: o.thinking,
		})

		var text strings.Builder
		var toolUses []conversation.ToolUseBlock
		var streamEnd llm.StreamEnd
		var streamErr error

	forward:
		for ev := range events {
			switch e := ev.(type) {
			case llm.TextDelta:
				text.WriteString(e.Text)
				if !send(TextDelta{Text: e.Text}) {
					return
				}
			case llm.ThinkingDelta:
				if !send(ThinkingDelta{Text: e.Text}) {
					return
				}
			case llm.ThinkingComplete:
				if !send(ThinkingComplete{Text: e.Thinking, Signature: e.Signature}) {
					return
				}
			case llm.ToolCallComplete:
				toolUses = append(toolUses, conversation.ToolUseBlock{
					ID:   e.ToolID,
					Name: e.ToolName,
					Args: e.Arguments,
				})
				if !send(ToolCallBegin{ToolID: e.ToolID, ToolName: e.ToolName, Args: e.Arguments}) {
					return
				}
			case llm.StreamEnd:
				streamEnd = e
				break forward
			case llm.StreamError:
				streamErr = e.Err
				break forward
			}
		}

		switch {
		case streamErr != nil:
			send(TurnError{Err: streamErr})
			return
		case ctx.Err() != nil:
			return
		}

		// Commit the assistant message before executing tools so the history
		// is coherent even if an execution panics the process later. Plain
		// replies take the text path; tool turns carry blocks.
		if len(toolUses) == 0 {
			o.conv.AddAssistantMessage(text.String())
		} else {
			var blocks []conversation.Block
			if text.Len() > 0 {
				blocks = append(blocks, conversation.TextBlock{Text: text.String()})
			}
			for _, tu := range toolUses {
				blocks = append(blocks, tu)
			}
			o.conv.AddAssistantBlocks(blocks...)
		}

		if len(toolUses) == 0 {
			send(TurnEnd{StopReason: streamEnd.StopReason, Usage: streamEnd.Usage})
			return
		}

		// Single tool round: execute every call in order, feed results back,
		// stop. The loop that re-requests after this lands in a later phase.
		results := make([]conversation.ToolResultBlock, 0, len(toolUses))
		for _, tu := range toolUses {
			start := time.Now()
			var result tools.ToolResult
			if tool := o.registry.Get(tu.Name); tool == nil {
				result = tools.ToolResult{
					Output:  "unknown tool " + tu.Name + "; pick one of the tools offered",
					IsError: true,
				}
			} else {
				result = tool.Execute(ctx, tu.Args)
			}
			rb := conversation.ToolResultBlock{
				ToolUseID: tu.ID,
				Content:   result.Output,
				IsError:   result.IsError,
			}
			results = append(results, rb)
			if !send(ToolCallEnd{
				ToolID:   tu.ID,
				ToolName: tu.Name,
				Result:   result,
				Elapsed:  time.Since(start),
			}) {
				return
			}
		}
		o.conv.AddToolResults(results...)
		send(TurnEnd{StopReason: streamEnd.StopReason, Usage: streamEnd.Usage})
	}()
	return ch
}
