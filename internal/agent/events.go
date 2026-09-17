package agent

import (
	"time"

	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/permissions"
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

type PermissionRequest struct {
	ToolName string             // the tool the call would use
	Action   string             // what the call would do: the command line or the path
	Rules    []permissions.Rule // what "session" and "permanent" would record
	Response chan<- PermissionResponse
}

type PermissionRequestEvent struct{ Request PermissionRequest }

type PermissionResponse int

const (
	PermAllowOnce      PermissionResponse = iota // run this call; ask again next time
	PermAllowSession                             // record a grant until the process exits
	PermAllowPermanent                           // write the grant to the local rule file
	PermReject                                   // do not run this call
)

func (TextDelta) agentEvent()              {}
func (TextComplete) agentEvent()           {}
func (ThinkingDelta) agentEvent()          {}
func (ThinkingComplete) agentEvent()       {}
func (ToolCallBegin) agentEvent()          {}
func (ToolCallEnd) agentEvent()            {}
func (LoopComplete) agentEvent()           {}
func (LoopError) agentEvent()              {}
func (PermissionRequestEvent) agentEvent() {}
