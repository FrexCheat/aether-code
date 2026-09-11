package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/FrexCheat/aether-code/internal/llm"
)

// liveStream is the lifecycle of one in-flight turn: the event channel and
// its cancel func. Model holds *liveStream; nil means idle — there is no
// separate streaming flag to keep in sync.
type liveStream struct {
	events <-chan llm.StreamEvent
	cancel context.CancelFunc
}

// streamBatchMsg carries events drained in one frame. waitStream blocks for
// the first event, then non-blockingly drains whatever else has already
// arrived so a burst of deltas becomes a single Update.
type streamBatchMsg []llm.StreamEvent

type tickMsg time.Time

// tickInterval is the timer refresh period; tests shorten it.
var tickInterval = time.Second

// waitStream bridges the provider goroutine into the Elm loop with
// coalescing: one blocking read, then a non-blocking drain of the backlog.
// A closed channel yields nil (no message); dialed-back frames keep arriving
// via tick while the stream is live.
func waitStream(ch <-chan llm.StreamEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		batch := []llm.StreamEvent{ev}
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
