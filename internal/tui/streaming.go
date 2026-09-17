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
	ctx    context.Context
	cancel context.CancelFunc
	text   strings.Builder
	start  time.Time

	permissionWait  time.Duration
	permissionSince time.Time
}

func (ls *liveStream) ran() time.Duration {
	return timeSince(ls.start) - ls.permissionWait
}

type streamBatchMsg []agent.AgentEvent

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
