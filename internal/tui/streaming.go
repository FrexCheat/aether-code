package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/FrexCheat/aether-code/internal/agent"
)

// liveStream is the lifecycle of one in-flight turn: the event channel, its
// context and cancel func, the accumulated text deltas (committed at each
// text block's end), the turn counter fed by TurnComplete events, and the
// timing start of the whole agent loop. Model holds it via *liveStream; nil
// means idle. History commits happen inside the agent; the TUI only renders.
type liveStream struct {
	events <-chan agent.AgentEvent
	ctx    context.Context // mirrors the ctx passed to RunLoop; nil Err means still running
	cancel context.CancelFunc
	text   strings.Builder
	turn   int // executed tool rounds so far, from TurnComplete events
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
