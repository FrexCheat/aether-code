package agent

import (
	"time"

	"github.com/FrexCheat/aether-code/internal/compaction"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/tools"
)

// eventSink hands one event to the UI. False means the event was not delivered
// because the turn was cancelled first.
type eventSink func(Event) bool

type Event interface{ event() }

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
	ToolRounds int
	Usage      llm.Usage // the turn's last request, not a sum over its rounds
}

type LoopError struct{ Err error }

// CompactionProgress carries one compaction progress step; the payload type
// stays in the compaction package so the vocabulary has a single definition.
type CompactionProgress struct{ Progress compaction.Progress }

// PermissionRequestEvent carries a question the loop is blocked on; the call
// proceeds only once the answer is sent on Request.Reply.
type PermissionRequestEvent struct{ Request permissions.Request }

func (TextDelta) event()              {}
func (TextComplete) event()           {}
func (ThinkingDelta) event()          {}
func (ThinkingComplete) event()       {}
func (ToolCallBegin) event()          {}
func (ToolCallEnd) event()            {}
func (LoopComplete) event()           {}
func (LoopError) event()              {}
func (PermissionRequestEvent) event() {}
func (CompactionProgress) event()     {}
