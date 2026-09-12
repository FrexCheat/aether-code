package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/FrexCheat/aether-code/internal/agent"
)

// liveStream is the lifecycle of one in-flight turn: the event channel, its
// cancel func, the user's question, the accumulated text deltas (committed
// whole on completion) and the timing start. Model holds it via *liveStream;
// nil means idle. History commits happen inside the agent; the TUI only
// renders.
type liveStream struct {
	events   <-chan agent.AgentEvent
	cancel   context.CancelFunc
	question string
	text     strings.Builder
	start    time.Time
}

// streamBatchMsg carries events drained in one frame. waitStream blocks for
// the first event, then non-blockingly drains whatever else has already
// arrived so a burst of deltas becomes a single Update.
type streamBatchMsg []agent.AgentEvent

type tickMsg time.Time

// tickInterval is the timer/spinner refresh period; tests shorten it.
var tickInterval = 100 * time.Millisecond

// waitStream bridges the agent goroutine into the Elm loop with coalescing:
// one blocking read, then a non-blocking drain of the backlog. A closed
// channel yields nil (no message); dialed-back frames keep arriving via tick
// while the stream is live.
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

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}
