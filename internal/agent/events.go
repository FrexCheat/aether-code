package agent

import (
	"time"

	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/tools"
)

type AgentEvent interface{ agentEvent() }

type TextDelta struct{ Text string }

type TextComplete struct{}

type ThinkingDelta struct{ Text string }

type ThinkingComplete struct {
	Thinking  string
	Signature string
}

type ToolCallBegin struct {
	ToolID   string
	ToolName string
	Args     map[string]any
}

type ToolCallEnd struct {
	ToolID   string
	ToolName string
	Result   tools.ToolResult
	Elapsed  time.Duration
}

type LoopStopReason string

const (
	StoppedByModel LoopStopReason = "model_stop"     // the model stopped requesting tools; normal end
	StoppedByLimit LoopStopReason = "max_iterations" // tool-round ceiling reached
	StoppedByUser  LoopStopReason = "cancelled"      // user cancelled the turn
)

type LoopComplete struct {
	Reason     LoopStopReason
	TotalTurns int
	Usage      llm.UsageInfo
}

type LoopError struct{ Err error }

func (TextDelta) agentEvent()        {}
func (TextComplete) agentEvent()     {}
func (ThinkingDelta) agentEvent()    {}
func (ThinkingComplete) agentEvent() {}
func (ToolCallBegin) agentEvent()    {}
func (ToolCallEnd) agentEvent()      {}
func (LoopComplete) agentEvent()     {}
func (LoopError) agentEvent()        {}
