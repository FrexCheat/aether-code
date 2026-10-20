// Package compaction spills large tool results and summarizes old history.
package compaction

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/tools"
)

const (
	summaryFailureLimit = 3

	// outputReserveTokens caps the room a summary call needs for its own
	// output (the p99.99 summary measures ~17.4k tokens); the model's actual
	// output cap wins when it is smaller. The margins sit below the usable
	// space: auto leaves extra slack for the summary round-trip, force is the
	// hard line, and warn fires warnMargin below auto.
	outputReserveTokens = 20000
	autoCompactMargin   = 13000
	forceMargin         = 3000
	warnMargin          = 20000
)

type thresholds struct {
	auto  int
	force int
}

func computeThresholds(contextWindow, maxOutput int) thresholds {
	reserve := outputReserveTokens
	if maxOutput > 0 && maxOutput < reserve {
		reserve = maxOutput
	}
	usable := contextWindow - reserve
	return thresholds{auto: usable - autoCompactMargin, force: usable - forceMargin}
}

type Level int

const (
	LevelOK Level = iota
	LevelWarn
	LevelForce
)

func (l Level) String() string {
	switch l {
	case LevelWarn:
		return "low"
	case LevelForce:
		return "critical"
	default:
		return "ok"
	}
}

type Phase int

const (
	PhaseStart Phase = iota
	PhaseDone
	PhaseFailed
)

type Progress struct {
	Phase                Phase
	Auto                 bool
	FromTokens, ToTokens int
	Err                  error
}

type failBand int

const (
	bandAuto failBand = iota
	bandForce
)

type Result struct {
	Compacted            bool
	FromTokens, ToTokens int
}

type ManagerParams struct {
	Client        llm.Client
	Conv          *conversation.Manager
	Schemas       func() []tools.ToolSchema
	SystemPrompt  string
	ContextWindow int
	MaxOutput     int
	SpillDir      string
	// Recovery restores instructions the summary replaced.
	Recovery func() string
}

type Manager struct {
	mu            sync.Mutex
	client        llm.Client
	conv          *conversation.Manager
	schemas       func() []tools.ToolSchema
	recovery      func() string
	estimator     *Estimator
	contextWindow int
	thresholds    thresholds
	spillDir      string
	anchor        UsageAnchor
	failures      int
	forceFailures int
	// Prepare and ForceCompact hold mu across the summarize call.
	used atomic.Int64
}

func NewManager(p ManagerParams) *Manager {
	return &Manager{
		client:        p.Client,
		conv:          p.Conv,
		schemas:       p.Schemas,
		recovery:      p.Recovery,
		estimator:     NewEstimator(p.SystemPrompt, p.Schemas),
		contextWindow: p.ContextWindow,
		thresholds:    computeThresholds(p.ContextWindow, p.MaxOutput),
		spillDir:      p.SpillDir,
	}
}

func (m *Manager) Level() Level {
	used := int(m.used.Load())
	switch {
	case used >= m.thresholds.force:
		return LevelForce
	case used >= m.thresholds.auto-warnMargin:
		return LevelWarn
	default:
		return LevelOK
	}
}

func (m *Manager) countFailure(band failBand) {
	if band == bandForce {
		m.forceFailures++
		return
	}
	m.failures++
}

func (m *Manager) UsedTokens() int { return int(m.used.Load()) }

func (m *Manager) ContextWindow() int { return m.contextWindow }

func (m *Manager) publish() { m.used.Store(int64(m.usedTokens())) }

// RecordUsage re-anchors on a completed request. Call it right after the
// assistant message is committed and before the round's tool results are
// appended: then AnchorCount covers everything the request carried, and the
// Baseline's output side accounts for the assistant message about to be
// replayed.
func (m *Manager) RecordUsage(u llm.Usage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if total := u.TotalTokens(); total > 0 {
		m.anchor = UsageAnchor{BaselineTokens: total, AnchorCount: m.conv.Len()}
	} else {
		// A provider or proxy that stopped reporting must not pin the estimate
		// to a stale baseline; dropping the anchor falls back to the full
		// estimate, which at least keeps moving.
		m.anchor = UsageAnchor{}
	}
	m.publish()
}

func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.anchor = UsageAnchor{}
	m.failures = 0
	m.forceFailures = 0
	m.used.Store(0)
}

func (m *Manager) SetSpillDir(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spillDir = dir
}

func (m *Manager) usedTokens() int {
	msgs := m.conv.Messages()
	if m.anchor.AnchorCount > 0 && m.anchor.AnchorCount <= len(msgs) {
		return m.anchor.BaselineTokens + m.estimator.Messages(msgs[m.anchor.AnchorCount:])
	}
	return m.estimator.FixedPrefix() + m.estimator.Messages(msgs)
}

// Prepare runs before every API request. Layer 1 always; Layer 2 at the
// force line while the force band's breaker is open, or at the auto line
// while the auto band's is. A summary failure never fails the request.
func (m *Manager) Prepare(ctx context.Context, notify func(Progress)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spillOversized()
	m.publish()
	used := m.usedTokens()
	if used >= m.thresholds.force {
		if m.forceFailures >= summaryFailureLimit {
			return
		}
		_, _ = m.compact(ctx, notify, true, bandForce)
		return
	}
	if used < m.thresholds.auto || m.failures >= summaryFailureLimit {
		return
	}
	_, _ = m.compact(ctx, notify, true, bandAuto)
}

// ForceCompact runs one spill + summarize pass regardless of the breakers —
// /compact and the prompt-too-long retry share it — but a failure still
// feeds the force band, so a doomed summarizer cannot be re-run every round
// through the retry path.
func (m *Manager) ForceCompact(ctx context.Context, notify func(Progress)) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spillOversized()
	m.publish()
	return m.compact(ctx, notify, false, bandForce)
}
