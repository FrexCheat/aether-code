// Package compaction owns the two-layer context budget: Layer 1 spills
// oversized tool results to disk before every request, Layer 2 summarizes
// older history once the estimated context nears the window. Token usage is
// anchored on the last real API accounting; only the increments since it are
// char-estimated.
package compaction

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/tools"
)

const summaryFailureLimit = 3

type Event interface{ event() }

// StartEvent fires once a summary call is actually launched; Auto marks the
// pre-request path (false: /compact or a prompt-too-long retry).
type StartEvent struct{ Auto bool }

type DoneEvent struct {
	Auto                 bool
	FromTokens, ToTokens int
}

type FailEvent struct {
	Auto bool
	Err  error
}

func (StartEvent) event() {}
func (DoneEvent) event()  {}
func (FailEvent) event()  {}

// Result reports one ForceCompact run. Compacted=false means there was no
// history worth summarizing (or nothing at all).
type Result struct {
	Compacted            bool
	FromTokens, ToTokens int
}

type Manager struct {
	mu            sync.Mutex
	client        llm.Client
	conv          *conversation.Manager
	schemas       func() []tools.ToolSchema
	contextWindow int
	spillDir      string
	anchor        UsageAnchor
	failures      int
}

func NewManager(client llm.Client, conv *conversation.Manager, schemas func() []tools.ToolSchema, contextWindow int, workDir string) *Manager {
	return &Manager{
		client:        client,
		conv:          conv,
		schemas:       schemas,
		contextWindow: contextWindow,
		spillDir:      filepath.Join(workDir, ".aethercode", "tool-results", strconv.Itoa(os.Getpid())),
	}
}

// RecordUsage re-anchors on a completed request. Call it right after the
// assistant message is committed and before the round's tool results are
// appended: then AnchorCount covers everything the request carried, and the
// Baseline's output side accounts for the assistant message about to be
// replayed.
func (m *Manager) RecordUsage(u llm.Usage) {
	total := u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheCreationTokens
	if total == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.anchor = UsageAnchor{BaselineTokens: total, AnchorCount: m.conv.Len()}
}

// Reset drops the anchor and the failure count; called by /clear.
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.anchor = UsageAnchor{}
	m.failures = 0
}

// usedTokens is baseline-plus-increments while the anchor is valid, and a
// full estimate otherwise (first turn, or the history was rewritten).
func (m *Manager) usedTokens() int {
	msgs := m.conv.Messages()
	if m.anchor.AnchorCount > 0 && m.anchor.AnchorCount <= len(msgs) {
		return m.anchor.BaselineTokens + EstimateMessages(msgs[m.anchor.AnchorCount:])
	}
	return EstimateMessages(msgs)
}

// Prepare runs before every API request. Layer 1 always; Layer 2 only at the
// auto line with the breaker open, or at the force line where the breaker is
// ignored. A summary failure never fails the request.
func (m *Manager) Prepare(ctx context.Context, notify func(Event)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spillOversized()
	used := m.usedTokens()
	effective := m.contextWindow - outputReserveTokens
	atAuto := used >= effective-autoCompactMargin
	atForce := used >= effective-forceMargin
	if !atAuto && !atForce {
		return
	}
	if m.failures >= summaryFailureLimit && !atForce {
		return
	}
	_, _ = m.compact(ctx, notify, true)
}

// ForceCompact runs one spill + summarize pass regardless of the breaker;
// /compact and the prompt-too-long retry share it.
func (m *Manager) ForceCompact(ctx context.Context, notify func(Event)) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spillOversized()
	return m.compact(ctx, notify, false)
}
