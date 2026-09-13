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

// defaultMaxRounds backs New when the caller passes a non-positive ceiling,
// so even a misconfigured construction can never loop forever.
const defaultMaxRounds = 50

// unknownToolLimit is how many consecutive calls to unregistered tools end
// the loop; executing any known tool resets the streak.
const unknownToolLimit = 3

// Orchestrator wires the pieces one session talks to. The conversation
// manager is injected, not owned: the TUI keeps its handle on history.
type Orchestrator struct {
	client    llm.Client
	registry  *tools.Registry
	conv      *conversation.Manager
	system    string
	thinking  bool
	maxRounds int // executed-tool-round ceiling for one loop
	mode      Mode
}

// New builds an orchestrator. maxRounds caps the tool rounds of one loop.
func New(client llm.Client, registry *tools.Registry, conv *conversation.Manager,
	system string, thinking bool, maxRounds int) *Orchestrator {
	if maxRounds <= 0 {
		maxRounds = defaultMaxRounds
	}
	return &Orchestrator{
		client:    client,
		registry:  registry,
		conv:      conv,
		system:    system,
		thinking:  thinking,
		maxRounds: maxRounds,
		mode:      ModeNormal,
	}
}

// Mode reports the current mode.
func (o *Orchestrator) Mode() Mode { return o.mode }

// SetMode switches the tool set and prompt segment used by subsequent
// requests. It must be called while no loop is running (the TUI only does:
// slash commands are ignored mid-turn), so no synchronization is needed.
func (o *Orchestrator) SetMode(m Mode) { o.mode = m }

// requestSystem returns the system prompt for the current mode: plan mode
// appends the plan-mode guidance to the base prompt.
func (o *Orchestrator) requestSystem() string {
	if o.mode == ModePlan {
		return o.system + "\n\n" + prompt.PlanSegment
	}
	return o.system
}

// requestTools returns the tool schema list for the current mode: everything
// in normal mode, read-only tools in plan mode.
func (o *Orchestrator) requestTools() []map[string]any {
	if o.mode == ModePlan {
		return o.registry.FilteredSchemas(func(c tools.Category) bool { return c == tools.CategoryRead })
	}
	return o.registry.GetAllSchemas()
}

// RunLoop runs one user turn as a full agent loop: user message → stream →
// while the model asks for tools, execute them (reads concurrently, writes
// and commands serially) and feed the results back → repeat until it stops.
//
// The returned channel closes after exactly one terminal event: a
// LoopComplete on every non-stream-error path, a TurnError when a streaming
// request fails. Cancellation follows ctx; a cancelled loop ends with
// LoopComplete{OutcomeCancelled}, never TurnError.
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

		for turn := 1; ; turn++ {
			// Repair the tool_use/tool_result pairing at the request boundary so
			// history left dangling by an interrupted run still yields a legal
			// request; the repair is never written back into the history.
			msgs := conversation.EnsureToolPairing(o.conv.GetMessages())
			events := o.client.Stream(ctx, llm.Request{
				System:   o.requestSystem(),
				Messages: msgs,
				Tools:    o.requestTools(),
				Thinking: o.thinking,
			})

			var text strings.Builder
			var thinking []conversation.ThinkingBlock
			var toolUses []conversation.ToolUseBlock
			var streamEnd llm.StreamEnd
			var streamErr error
			cancelled := false

		forward:
			for ev := range events {
				switch e := ev.(type) {
				case llm.TextDelta:
					text.WriteString(e.Text)
					if !send(TextDelta{Text: e.Text}) {
						cancelled = true
						break forward
					}
				case llm.TextComplete:
					if !send(TextComplete{}) {
						cancelled = true
						break forward
					}
				case llm.ThinkingDelta:
					if !send(ThinkingDelta{Text: e.Text}) {
						cancelled = true
						break forward
					}
				case llm.ThinkingComplete:
					thinking = append(thinking, conversation.ThinkingBlock{
						Text:      e.Thinking,
						Signature: e.Signature,
					})
					if !send(ThinkingComplete{Text: e.Thinking, Signature: e.Signature}) {
						cancelled = true
						break forward
					}
				case llm.ToolCallComplete:
					toolUses = append(toolUses, conversation.ToolUseBlock{
						ID:   e.ToolID,
						Name: e.ToolName,
						Args: e.Arguments,
					})
					if !send(ToolCallBegin{ToolID: e.ToolID, ToolName: e.ToolName, Args: e.Arguments}) {
						cancelled = true
						break forward
					}
				case llm.StreamEnd:
					streamEnd = e
					break forward
				case llm.StreamError:
					streamErr = e.Err
					break forward
				}
			}

			// Cancellation outranks stream errors: a cancelled request
			// surfaces as a StreamError carrying ctx.Err(), but the user
			// action is a cancel, not a failure.
			if cancelled || ctx.Err() != nil {
				// Keep what arrived and keep the history pair-complete: the
				// assistant turn (partial text, any fully-announced calls)
				// commits, every announced-but-unexecuted call gets a
				// synthetic failure result.
				o.commitAssistant(text.String(), thinking, toolUses)
				o.commitSyntheticResults(toolUses, "cancelled before execution")
				terminal(LoopComplete{Outcome: OutcomeCancelled, TotalTurns: turns, Usage: usage})
				return
			}
			if streamErr != nil {
				terminal(TurnError{Err: streamErr})
				return
			}
			usage = addUsage(usage, streamEnd.Usage)

			// Commit the assistant message before executing tools so the
			// history is coherent even if an execution panics the process
			// later. Tool turns carry the thinking segments too: replaying
			// them (with the provider's signature) is required to continue a
			// thinking chain.
			o.commitAssistant(text.String(), thinking, toolUses)

			if len(toolUses) == 0 {
				terminal(LoopComplete{Outcome: OutcomeModelStop, TotalTurns: turns, Usage: usage})
				return
			}

			// Guard rail: the round ceiling counts executed tool rounds. The
			// model's calls stay unexecuted; synthetic failure results keep
			// the history pair-complete and tell the model why.
			if turns >= o.maxRounds {
				o.commitSyntheticResults(toolUses, "iteration limit reached before this call could run")
				terminal(LoopComplete{Outcome: OutcomeMaxIterations, TotalTurns: turns, Usage: usage})
				return
			}

			// Execute this turn's calls. Events go out in original call
			// order (per batch join); results are collected in the same
			// order regardless of completion timing.
			outcomes := executeToolCalls(ctx, toolUses, o.registry, func(i int, oc execOutcome) {
				send(ToolCallEnd{
					ToolID:   toolUses[i].ID,
					ToolName: toolUses[i].Name,
					Result:   oc.Result,
					Elapsed:  oc.Elapsed,
				})
			})

			results := make([]conversation.ToolResultBlock, 0, len(outcomes))
			for i, oc := range outcomes {
				results = append(results, conversation.ToolResultBlock{
					ToolUseID: toolUses[i].ID,
					Content:   oc.Result.Output,
					IsError:   oc.Result.IsError,
				})
			}
			o.conv.AddToolResults(results...)
			turns++

			// Unknown-tool streak, walked in call order so "consecutive"
			// means consecutive in the model's own sequence. Known-tool
			// executions reset it; reaching the limit ends the loop before
			// the next request.
			for _, oc := range outcomes {
				if oc.Unknown {
					unknownStreak++
					if unknownStreak >= unknownToolLimit {
						break
					}
				} else {
					unknownStreak = 0
				}
			}
			if unknownStreak >= unknownToolLimit {
				terminal(LoopComplete{Outcome: OutcomeUnknownTool, TotalTurns: turns, Usage: usage})
				return
			}

			// The turn is done: results are on record, the next iteration is
			// the next turn. A failed send here is the cancellation path
			// below finishing the loop.
			send(TurnComplete{Turn: turns})
			if ctx.Err() != nil {
				terminal(LoopComplete{Outcome: OutcomeCancelled, TotalTurns: turns, Usage: usage})
				return
			}
		}
	}()
	return ch
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
