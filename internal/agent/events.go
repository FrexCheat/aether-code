package agent

import (
	"time"

	"github.com/FrexCheat/aether-code/internal/compaction"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/tools"
)

type eventSink func(TurnEvent) bool

type TurnEvent interface{ turnEvent() }

type TextDelta struct{ Text string }

type TextComplete struct{}

type ThinkingKind uint8

const (
	ThinkingUnknown ThinkingKind = iota
	ThinkingSummary
	ThinkingContent
)

type ThinkingDelta struct {
	Kind ThinkingKind
	Text string
}

type ThinkingCompleted struct {
	Summary string
	Content string
}

type ToolStarted struct {
	ToolID   string
	ToolName string
	Args     map[string]any
}

type ToolFinished struct {
	ToolID   string
	ToolName string
	Result   tools.ToolResult
	Elapsed  time.Duration
}

type ContextUsage struct {
	UsedTokens   int
	WindowTokens int
	Level        compaction.Level
}

type UsageUpdated struct {
	Usage   llm.Usage
	Context ContextUsage
}

type PermissionRequested struct {
	ID     string
	Prompt permissions.Prompt
}

type CompactionProgress struct{ Progress compaction.Progress }

type StopReason string

const (
	StoppedByModel StopReason = "model_stop"
	StoppedByLimit StopReason = "max_iterations"
	StoppedByUser  StopReason = "cancelled"
)

type TurnCompleted struct {
	Reason     StopReason
	ToolRounds int
}

type TurnFailed struct{ Err error }

func (TextDelta) turnEvent()           {}
func (TextComplete) turnEvent()        {}
func (ThinkingDelta) turnEvent()       {}
func (ThinkingCompleted) turnEvent()   {}
func (ToolStarted) turnEvent()         {}
func (ToolFinished) turnEvent()        {}
func (UsageUpdated) turnEvent()        {}
func (PermissionRequested) turnEvent() {}
func (CompactionProgress) turnEvent()  {}
func (TurnCompleted) turnEvent()       {}
func (TurnFailed) turnEvent()          {}
