package tui

import (
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/compaction"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/tools"
)

type turnTestCompactor struct {
	used   int
	window int
	level  compaction.Level
}

func (c turnTestCompactor) UsedTokens() int    { return c.used }
func (c turnTestCompactor) ContextWindow() int { return c.window }
func (c turnTestCompactor) Level() compaction.Level {
	return c.level
}
func (c turnTestCompactor) ForceCompact(context.Context, func(compaction.Progress)) (compaction.Result, error) {
	return compaction.Result{}, nil
}
func (c turnTestCompactor) Reset()             {}
func (c turnTestCompactor) SetSpillDir(string) {}

type immediateTurnClient struct{}

func (immediateTurnClient) Stream(context.Context, llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.TextDelta{Text: "hello"}, nil) {
			return
		}
		yield(llm.StreamEnd{}, nil)
	}
}

func TestUsageUpdatedRefreshesFooterImmediately(t *testing.T) {
	m := dispatchModel(t)
	m.opts.Compactor = turnTestCompactor{used: 250, window: 1000, level: compaction.LevelWarn}
	m.foldEvent(agent.UsageUpdated{
		Usage: llm.Usage{InputTokens: 50, CacheReadTokens: 50},
	})
	if got := cacheHitRate(m.lastUsage); got != 50 {
		t.Fatalf("cache hit = %d, want 50", got)
	}
	footer := m.footer().Usage
	if !strings.Contains(footer, "250 / 1k") || !strings.Contains(footer, "context low") || !strings.Contains(footer, "50% cache hit") {
		t.Errorf("footer = %q, want current usage and cache hit", footer)
	}
}

func TestThinkingEventsHaveNoTUIOutput(t *testing.T) {
	m := dispatchModel(t)
	m.live.Begin()
	before := len(m.transcript.blocks)
	m.foldEvent(agent.ThinkingDelta{Kind: agent.ThinkingSummary, Text: "why"})
	m.foldEvent(agent.ThinkingCompleted{Summary: "why", Content: "detail"})
	if len(m.transcript.blocks) != before {
		t.Errorf("thinking emitted %d transcript blocks", len(m.transcript.blocks)-before)
	}
	if text := m.live.TakeText(); text != "" {
		t.Errorf("thinking leaked into the live answer: %q", text)
	}
}

func TestTurnStreamClosesAfterTerminalEvent(t *testing.T) {
	ag := agent.New(immediateTurnClient{}, tools.NewRegistry(), nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := startTurnStream(ag.StartTurn(ctx, conversation.NewManager()), cancel)

	var events []agent.TurnEvent
	for event := range stream.events {
		events = append(events, event)
	}
	if len(events) == 0 {
		t.Fatal("turn stream closed without events")
	}
	if _, ok := events[len(events)-1].(agent.TurnCompleted); !ok {
		t.Errorf("terminal event = %T, want TurnCompleted", events[len(events)-1])
	}
	stream.Close()
}
