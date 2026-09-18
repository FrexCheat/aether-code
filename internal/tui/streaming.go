package tui

import (
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/FrexCheat/aether-code/internal/agent"
)

type toolCall struct {
	id    string
	title string
}

// liveStream is one turn in flight: the events still arriving, what it has
// produced so far, and how long it has been running.
type liveStream struct {
	events <-chan agent.Event
	cancel func()
	text   strings.Builder
	tools  []toolCall
	start  time.Time

	permissionWait  time.Duration
	permissionSince time.Time
}

func (ls *liveStream) elapsed() time.Duration {
	return time.Since(ls.start) - ls.permissionWait
}

func (ls *liveStream) beginTool(id, title string) {
	ls.tools = append(ls.tools, toolCall{id: id, title: title})
}

func (ls *liveStream) endTool(id string) (toolCall, bool) {
	for i, call := range ls.tools {
		if call.id == id {
			ls.tools = slices.Delete(ls.tools, i, i+1)
			return call, true
		}
	}
	return toolCall{}, false
}

type streamBatchMsg []agent.Event

func waitStream(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		batch := []agent.Event{ev}
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
