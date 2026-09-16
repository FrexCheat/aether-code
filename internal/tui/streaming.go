package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/FrexCheat/aether-code/internal/agent"
)

type liveStream struct {
	events <-chan agent.AgentEvent
	ctx    context.Context // mirrors the ctx passed to Agent.Run; nil Err means still running
	cancel context.CancelFunc
	text   strings.Builder
	start  time.Time
}

// streamBatchMsg carries events drained in one frame. waitStream blocks for
// the first event, then non-blockingly drains whatever else has already
// arrived so a burst of deltas becomes a single Update. While the stream is
// live the spinner's own tick chain keeps frames arriving.
type streamBatchMsg []agent.AgentEvent

// waitStream bridges the agent goroutine into the Elm loop with coalescing:
// one blocking read, then a non-blocking drain of the backlog. A closed
// channel yields nil (no message).
func waitStream(ch <-chan agent.AgentEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		batch := []agent.AgentEvent{ev}
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return streamBatchMsg(batch)
				}
				batch = append(batch, ev)
			default:
				return streamBatchMsg(batch)
			}
		}
	}
}
