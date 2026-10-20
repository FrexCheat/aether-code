package components

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const frameInterval = time.Second / 10

type TickMsg struct{ seq int }

type Clock struct {
	frame   int
	running bool
	seq     int
}

type Stopwatch struct {
	started  time.Time
	pausedAt time.Time
	paused   time.Duration
}

func (c *Clock) Start() tea.Cmd {
	if c.running {
		return nil
	}
	c.running = true
	c.seq++
	return c.tick()
}

func (c *Clock) Stop() { c.running = false }

func (c Clock) Frame() int { return c.frame }

// A tick from an older chain is stale and must not double the clock's rate.
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

func (s *Stopwatch) Start() {
	s.started = time.Now()
	s.pausedAt = time.Time{}
	s.paused = 0
}

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
