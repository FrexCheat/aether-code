// Package compaction owns the two-layer context budget: Layer 1 spills
// oversized tool results to disk before every request, Layer 2 summarizes
// older history once the estimated context nears the window. Token usage is
// anchored on the last real API accounting; only the increments since it are
// char-estimated.
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

// thresholds are the trigger lines in tokens, precomputed once at
// construction so reads never recompute them.
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

// Level classifies the last published estimate against the lines.
type Level int

const (
	LevelOK Level = iota
	LevelWarn
	LevelForce
)

// String names the level for display; these three words are the vocabulary the
// status readouts share, so they live with the level rather than in each
// caller.
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

// Phase names one step of a compaction run.
type Phase int

const (
	PhaseStart Phase = iota
	PhaseDone
	PhaseFailed
)

// Progress reports one compaction step. Auto marks the pre-request path
// (false: /compact or a prompt-too-long retry).
type Progress struct {
	Phase                Phase
	Auto                 bool
	FromTokens, ToTokens int
	Err                  error
}

// failBand names the circuit-breaker counter a failed summary feeds: the
// pre-request bands count separately so a permanently failing summarizer at
// the force line cannot re-run every round, while the auto band keeps its
// own three-strike budget.
type failBand int

const (
	bandAuto failBand = iota
	bandForce
)

// Result reports one ForceCompact run. Compacted=false means there was no
// history worth summarizing (or nothing at all).
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
	// Recovery supplies the text a boundary message puts back alongside the
	// summary: instructions the session loaded explicitly and the summary
	// silently replaced. The host wires it to whatever keeps that ledger, so
	// this package needs to know nothing about it. nil or an empty answer
	// leaves the boundary as the summary alone.
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
	// used publishes usedTokens for lock-free readers: Prepare and ForceCompact
	// hold mu across the summarize call, so the TUI cannot read the estimate
	// under the lock.
	used atomic.Int64
}

// NewManager deliberately publishes nothing: the readout reports what the
// provider accounted for this run, so a fresh or resumed session reads zero
// until its first turn, not a pre-request guess.
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

// Level reports where the last published estimate sits; it reads the atomic
// snapshot and never blocks behind a compaction in flight.
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

// countFailure feeds the band's breaker; any success clears both.
func (m *Manager) countFailure(band failBand) {
	if band == bandForce {
		m.forceFailures++
		return
	}
	m.failures++
}

// UsedTokens is the last published context estimate; safe to call at any time.
func (m *Manager) UsedTokens() int { return int(m.used.Load()) }

// ContextWindow is the provider's window the estimate is measured against.
func (m *Manager) ContextWindow() int { return m.contextWindow }

// publish refreshes the snapshot. The caller holds mu.
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

// Reset drops the anchor and both failure counts and returns the readout to
// the fresh-session state; called by /clear. It zeros the published snapshot
// instead of republishing the estimate: /clear starts an empty conversation, so
// the readout must match a fresh start — the fixed prefix is not "usage" until
// a request actually carries it.
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.anchor = UsageAnchor{}
	m.failures = 0
	m.forceFailures = 0
	m.used.Store(0)
}

// SetSpillDir points Layer 1 at another directory; /clear calls it when it
// rotates to a new session, since each session owns its spill files.
func (m *Manager) SetSpillDir(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spillDir = dir
}

// usedTokens is baseline-plus-increments while the anchor is valid, and a
// full estimate otherwise (first turn, or the history was rewritten). The
// full estimate includes the fixed prefix — system prompt and tools — which
// the anchor baseline already carries on the anchored path.
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
