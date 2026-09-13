// 事件契约：RunLoop 通过 channel 推送给 UI 的全部事件。类型定义与编排逻辑
// 分离，本文件是 TUI 依赖的完整事件面；llm 层的流式事件在 RunLoop 内消费，
// 不会越过这一层到达 UI。
package agent

import (
	"time"

	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/tools"
)

// AgentEvent is the sealed event stream RunLoop feeds to the UI. Text and
// thinking events forward the provider stream; tool events bracket each
// execution; TurnComplete reports each finished tool round; LoopComplete /
// LoopError are terminal and close the channel.
type AgentEvent interface{ agentEvent() }

type TextDelta struct{ Text string }

// TextComplete marks the end of one text content block. The UI commits the
// accumulated reply as one assistant entry when it arrives, so a reply
// segment spoken before a tool call displays above that call.
type TextComplete struct{}

type ThinkingDelta struct{ Text string }

type ThinkingComplete struct {
	Thinking  string
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

// LoopOutcome expresses why a loop ended. The Loop is the full sequence of
// turns — request → tools → results, repeated — up to and including the
// response without tool calls; a Turn is one iteration of it.
type LoopOutcome string

const (
	OutcomeModelStop     LoopOutcome = "model_stop"     // the model stopped requesting tools; normal end
	OutcomeMaxIterations LoopOutcome = "max_iterations" // tool-round ceiling reached
	OutcomeCancelled     LoopOutcome = "cancelled"      // user cancelled the turn
	OutcomeUnknownTool   LoopOutcome = "unknown_tool"   // too many consecutive unknown tools
)

// TurnComplete reports one finished turn — its tool calls executed and their
// results committed to history — just before the next request goes out. Turn
// counts executed tool rounds from 1. A pure reply (no tool calls) emits no
// TurnComplete.
type TurnComplete struct{ Turn int }

// LoopComplete is the single normal terminal event of a loop: the model
// stopped requesting tools (OutcomeModelStop) or a guard fired (the other
// outcomes, presented as non-errors). TotalTurns is the number of executed
// tool rounds (0 for a pure reply); Usage accumulates every completed round.
type LoopComplete struct {
	Outcome    LoopOutcome
	TotalTurns int
	Usage      llm.UsageInfo
}

// LoopError is the terminal event for a failed streaming request (network,
// auth, server errors). It is named for the Loop because it ends the whole
// loop, not just one turn. Cancellation is not an error: it ends with
// LoopComplete{OutcomeCancelled} instead.
type LoopError struct{ Err error }

func (TextDelta) agentEvent()        {}
func (TextComplete) agentEvent()     {}
func (ThinkingDelta) agentEvent()    {}
func (ThinkingComplete) agentEvent() {}
func (ToolCallBegin) agentEvent()    {}
func (ToolCallEnd) agentEvent()      {}
func (TurnComplete) agentEvent()     {}
func (LoopComplete) agentEvent()     {}
func (LoopError) agentEvent()        {}
