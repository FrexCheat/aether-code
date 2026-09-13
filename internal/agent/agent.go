// Package agent orchestrates a full agent loop end to end: it assembles the
// LLM request (history + tool list), consumes the provider stream, and while
// the model keeps asking for tools, executes them and feeds structured
// results back into the conversation before the next request.
//
// Terminology (docs/specs/agent-loop): a Turn is one iteration — one request
// plus its tool executions and result commit; the Loop is the whole sequence
// of turns up to and including the response without tool calls. RunLoop
// drives one Loop per user message and reports progress through the event
// stream defined in events.go.
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

// Mode selects which tools and prompt segment a loop's requests carry.
type Mode string

const (
	ModeNormal Mode = "normal"
	ModePlan   Mode = "plan"
)

// defaultMaxTurns backs New when the caller passes a non-positive ceiling,
// so even a misconfigured construction can never loop forever.
const defaultMaxTurns = 50

// unknownToolLimit is how many consecutive calls to unregistered tools end
// the loop; executing any known tool resets the streak.
const unknownToolLimit = 3

// Orchestrator wires the pieces one session talks to. The conversation
// manager is injected, not owned: the TUI keeps its handle on history.
type Orchestrator struct {
	client   llm.Client
	registry *tools.Registry
	conv     *conversation.Manager
	system   string
	thinking bool
	maxTurns int // turn ceiling for one loop

	// mu guards mode. The TUI writes it from slash commands and reads it
	// from the render path; the loop goroutine reads it per request — all
	// without ordering assumptions between caller and loop.
	mu   sync.RWMutex
	mode Mode
}

// New builds an orchestrator. maxTurns caps the tool rounds of one loop.
func New(client llm.Client, registry *tools.Registry, conv *conversation.Manager,
	system string, thinking bool, maxTurns int) *Orchestrator {
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

// Mode reports the current mode.
func (o *Orchestrator) Mode() Mode {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.mode
}

// SetMode switches the tool set and prompt segment used by subsequent
// requests. It is safe to call at any time: a running loop picks the new
// mode up at its next request boundary. The TUI currently only switches
// between loops (slash commands are ignored mid-turn).
func (o *Orchestrator) SetMode(m Mode) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.mode = m
}

// requestSystem returns the system prompt for the given mode: plan mode
// appends the plan-mode guidance to the base prompt.
func requestSystem(base string, mode Mode) string {
	if mode == ModePlan {
		return base + "\n\n" + prompt.PlanSegment
	}
	return base
}

// requestTools returns the tool schema list for the given mode: everything
// in normal mode, read-only tools in plan mode.
func requestTools(reg *tools.Registry, mode Mode) []tools.ToolSchema {
	if mode == ModePlan {
		return reg.FilteredSchemas(func(c tools.Category) bool { return c == tools.CategoryRead })
	}
	return reg.GetAllSchemas()
}

// RunLoop runs one user turn as a full agent loop: user message → stream →
// while the model asks for tools, execute them (reads concurrently, writes
// and commands serially) and feed the results back → repeat until it stops.
//
// The returned channel closes after exactly one terminal event: a
// LoopComplete on every non-stream-error path, a LoopError when a streaming
// request fails. Consumers must drain the channel until it closes — the
// terminal send does not consult ctx, which is what guarantees the contract.
// Cancellation follows ctx; a cancelled loop ends with
// LoopComplete{OutcomeCancelled}, never LoopError.
func (o *Orchestrator) RunLoop(ctx context.Context, userText string) <-chan AgentEvent {
	ch := make(chan AgentEvent, 64)
	go func() {
		defer close(ch)
		// send delivers regular events, giving up if ctx is cancelled; the
		// caller then routes to the cancel path below.
		send := func(ev AgentEvent) bool {
			select {
			case ch <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}
		// terminal delivers the one final event without consulting ctx: the
		// consumer drains the channel until the terminal event, so this send
		// always lands — including after a cancellation that aborted earlier
		// sends.
		terminal := func(ev AgentEvent) {
			ch <- ev
		}

		o.conv.AddUserMessage(userText)

		var turns int           // executed tool rounds
		var unknownStreak int   // consecutive unknown-tool calls across turns
		var usage llm.UsageInfo // accumulated over completed rounds

		for {
			// Repair the tool_use/tool_result pairing at the request boundary so
			// history left dangling by an interrupted run still yields a legal
			// request; the repair is never written back into the history.
			msgs := conversation.EnsureToolPairing(o.conv.GetMessages())
			// The mode is read per request, not pushed: a switch made between
			// turns takes effect at the next request boundary on its own.
			mode := o.Mode()
			events := o.client.Stream(ctx, llm.Request{
				System:   requestSystem(o.system, mode),
				Messages: msgs,
				Tools:    requestTools(o.registry, mode),
				Thinking: o.thinking,
			})

			ts := consumeStream(events, send)
			// Record the round's usage as soon as the stream reported it, so
			// the cancel branch below cannot drop the last round's tally.
			usage = addUsage(usage, ts.end.Usage)

			// Cancellation outranks stream errors: a cancelled request
			// surfaces as a StreamError carrying ctx.Err(), but the user
			// action is a cancel, not a failure.
			if ts.cancelled || ctx.Err() != nil {
				// Keep what arrived and keep the history pair-complete: the
				// assistant turn (partial text, any fully-announced calls)
				// commits, every announced-but-unexecuted call gets a
				// synthetic failure result.
				o.commitAssistant(ts.text.String(), ts.thinking, ts.toolUses)
				o.commitSyntheticResults(ts.toolUses, "cancelled before execution")
				terminal(LoopComplete{Outcome: OutcomeCancelled, TotalTurns: turns, Usage: usage})
				return
			}
			if ts.err != nil {
				terminal(LoopError{Err: ts.err})
				return
			}

			// Commit the assistant message before executing tools: the
			// history then already carries every announced call, so the
			// result commit below (real or synthetic) always finds its pair.
			// Tool turns carry the thinking segments too: replaying them
			// (with the provider's signature) is required to continue a
			// thinking chain.
			o.commitAssistant(ts.text.String(), ts.thinking, ts.toolUses)

			if len(ts.toolUses) == 0 {
				terminal(LoopComplete{Outcome: OutcomeModelStop, TotalTurns: turns, Usage: usage})
				return
			}

			// Guard rail: the ceiling counts executed tool rounds. The
			// model's calls stay unexecuted; synthetic failure results keep
			// the history pair-complete and tell the model why.
			if turns >= o.maxTurns {
				o.commitSyntheticResults(ts.toolUses, "iteration limit reached before this call could run")
				terminal(LoopComplete{Outcome: OutcomeMaxIterations, TotalTurns: turns, Usage: usage})
				return
			}

			// Execute this turn's calls. Events go out in original call
			// order (per batch join); results are collected in the same
			// order regardless of completion timing.
			outcomes := executeToolCalls(ctx, ts.toolUses, o.registry, func(i int, oc execOutcome) {
				send(ToolCallEnd{
					ToolID:   ts.toolUses[i].ID,
					ToolName: ts.toolUses[i].Name,
					Result:   oc.Result,
					Elapsed:  oc.Elapsed,
				})
			})
			o.conv.AddToolResults(toolResultBlocks(ts.toolUses, outcomes)...)
			turns++

			// The streak walks call order so "consecutive" means consecutive
			// in the model's own sequence; reaching the limit ends the loop
			// before the next request.
			unknownStreak = bumpUnknownStreak(unknownStreak, outcomes)
			if unknownStreak >= unknownToolLimit {
				terminal(LoopComplete{Outcome: OutcomeUnknownTool, TotalTurns: turns, Usage: usage})
				return
			}

			// The turn is done: results are on record, the next iteration is
			// the next turn. A failed send here means ctx is done; the check
			// below finishes the loop.
			send(TurnComplete{Turn: turns})
			if ctx.Err() != nil {
				terminal(LoopComplete{Outcome: OutcomeCancelled, TotalTurns: turns, Usage: usage})
				return
			}
		}
	}()
	return ch
}

// turnStream is one request's streamed output: the assistant turn parts
// accumulate here for the history commit while the events forward to the UI.
type turnStream struct {
	text      strings.Builder
	thinking  []conversation.ThinkingBlock
	toolUses  []conversation.ToolUseBlock
	end       llm.StreamEnd
	err       error
	cancelled bool // a send failed: ctx is done and the loop should unwind
}

// consumeStream drains one provider stream: text, thinking and tool events
// forward to the UI through send while their content accumulates for the
// history commit. It stops at StreamEnd or StreamError, or as soon as send
// reports cancellation — a cancelled stream may end without either terminal
// event.
func consumeStream(events <-chan llm.StreamEvent, send func(AgentEvent) bool) turnStream {
	var ts turnStream
forward:
	for ev := range events {
		switch e := ev.(type) {
		case llm.TextDelta:
			ts.text.WriteString(e.Text)
			if !send(TextDelta{Text: e.Text}) {
				ts.cancelled = true
				break forward
			}
		case llm.TextComplete:
			if !send(TextComplete{}) {
				ts.cancelled = true
				break forward
			}
		case llm.ThinkingDelta:
			if !send(ThinkingDelta{Text: e.Text}) {
				ts.cancelled = true
				break forward
			}
		case llm.ThinkingComplete:
			ts.thinking = append(ts.thinking, conversation.ThinkingBlock{
				Text:      e.Thinking,
				Signature: e.Signature,
			})
			if !send(ThinkingComplete{Thinking: e.Thinking, Signature: e.Signature}) {
				ts.cancelled = true
				break forward
			}
		case llm.ToolCallComplete:
			ts.toolUses = append(ts.toolUses, conversation.ToolUseBlock{
				ID:   e.ToolID,
				Name: e.ToolName,
				Args: e.Arguments,
			})
			if !send(ToolCallBegin{ToolID: e.ToolID, ToolName: e.ToolName, Args: e.Arguments}) {
				ts.cancelled = true
				break forward
			}
		case llm.StreamEnd:
			ts.end = e
			break forward
		case llm.StreamError:
			ts.err = e.Err
			break forward
		}
	}
	return ts
}

// toolResultBlocks pairs each outcome with its call, in original call order,
// ready for the history commit.
func toolResultBlocks(calls []conversation.ToolUseBlock, outcomes []execOutcome) []conversation.ToolResultBlock {
	results := make([]conversation.ToolResultBlock, 0, len(outcomes))
	for i, oc := range outcomes {
		results = append(results, conversation.ToolResultBlock{
			ToolUseID: calls[i].ID,
			Content:   oc.Result.Output,
			IsError:   oc.Result.IsError,
		})
	}
	return results
}

// bumpUnknownStreak walks one turn's outcomes in call order: known-tool
// executions reset the streak, unknown calls extend it.
func bumpUnknownStreak(streak int, outcomes []execOutcome) int {
	for _, oc := range outcomes {
		if oc.Unknown {
			streak++
		} else {
			streak = 0
		}
	}
	return streak
}

// commitAssistant appends the assistant turn for the current round: plain
// text when the model only produced text, the full block set when it also
// thought or called tools. A round with no content at all (stream ended
// without producing anything) commits nothing.
func (o *Orchestrator) commitAssistant(text string, thinking []conversation.ThinkingBlock, toolUses []conversation.ToolUseBlock) {
	if text == "" && len(thinking) == 0 && len(toolUses) == 0 {
		return
	}
	if len(thinking) == 0 && len(toolUses) == 0 {
		o.conv.AddAssistantMessage(text)
		return
	}
	o.conv.AddAssistantFull(text, thinking, toolUses)
}

// commitSyntheticResults appends IsError results for calls that will never
// get real ones (cancel or round ceiling), keeping the history pair-complete
// without relying on the request-boundary repair.
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

// addUsage sums two usage records field by field.
func addUsage(a, b llm.UsageInfo) llm.UsageInfo {
	return llm.UsageInfo{
		InputTokens:         a.InputTokens + b.InputTokens,
		OutputTokens:        a.OutputTokens + b.OutputTokens,
		CacheReadTokens:     a.CacheReadTokens + b.CacheReadTokens,
		CacheCreationTokens: a.CacheCreationTokens + b.CacheCreationTokens,
	}
}
