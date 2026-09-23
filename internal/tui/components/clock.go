package components

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// frameInterval is the cadence the shared clock advances at.
const frameInterval = time.Second / 10

// TickMsg advances the shared clock one frame. Only the clock produces it.
type TickMsg struct{ seq int }

// Clock is the one animation clock. It advances a frame counter on a fixed
// cadence and every animated row renders from that counter, so the rows stay in
// step and a single tick chain drives them all.
type Clock struct {
	frame   int
	running bool
	seq     int
}

// Stopwatch measures how long an activity has run. It pauses with the activity
// it reports — time spent waiting on a permission dialog is not activity time.
type Stopwatch struct {
	started  time.Time
	pausedAt time.Time
	paused   time.Duration
}

// Start begins the tick chain; it is a no-op while the clock already runs.
func (c *Clock) Start() tea.Cmd {
	if c.running {
		return nil
	}
	c.running = true
	c.seq++
	return c.tick()
}

// Stop ends the chain at its next tick.
func (c *Clock) Stop() { c.running = false }

// Frame is the current frame index.
func (c Clock) Frame() int { return c.frame }

// Update advances on its own tick and re-arms. A tick left over from a chain
// that has since restarted is stale and ends here instead of doubling the rate.
func (c *Clock) Update(msg tea.Msg) tea.Cmd {
	tick, ok := msg.(TickMsg)
	if !ok || !c.running || tick.seq != c.seq {
		return nil
	}
	c.frame++
	return c.tick()
}

func (c *Clock) tick() tea.Cmd {
	seq := c.seq
	return tea.Tick(frameInterval, func(time.Time) tea.Msg { return TickMsg{seq: seq} })
}

// Start resets the measurement and begins counting.
func (s *Stopwatch) Start() {
	s.started = time.Now()
	s.pausedAt = time.Time{}
	s.paused = 0
}

// Pause and Resume bracket an interruption that must not count.
func (s *Stopwatch) Pause() {
	if !s.started.IsZero() && s.pausedAt.IsZero() {
		s.pausedAt = time.Now()
	}
}

func (s *Stopwatch) Resume() {
	if !s.pausedAt.IsZero() {
		s.paused += time.Since(s.pausedAt)
		s.pausedAt = time.Time{}
	}
}

// Elapsed is the time measured so far; it keeps running until the next Start.
func (s Stopwatch) Elapsed() time.Duration {
	if s.started.IsZero() {
		return 0
	}
	elapsed := time.Since(s.started) - s.paused
	if !s.pausedAt.IsZero() {
		elapsed -= time.Since(s.pausedAt)
	}
	return elapsed
}
