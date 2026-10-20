package tui

import (
	"context"
	"fmt"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/compaction"
	"github.com/FrexCheat/aether-code/internal/format"
	"github.com/FrexCheat/aether-code/internal/permissions"
)

const turnEventBuffer = 64

type streamBatchMsg []agent.TurnEvent

type compactDoneMsg struct {
	result compaction.Result
	err    error
}

type turnStream struct {
	turn     *agent.Turn
	events   chan agent.TurnEvent
	cancel   context.CancelFunc
	stop     chan struct{}
	stopOnce sync.Once
}

func startTurnStream(turn *agent.Turn, cancel context.CancelFunc) *turnStream {
	stream := &turnStream{
		turn:   turn,
		events: make(chan agent.TurnEvent, turnEventBuffer),
		cancel: cancel,
		stop:   make(chan struct{}),
	}
	go stream.pump()
	return stream
}

func (s *turnStream) pump() {
	defer close(s.events)
	for event := range s.turn.Events() {
		select {
		case s.events <- event:
		case <-s.stop:
			return
		}
	}
}

func (s *turnStream) Cancel() { s.cancel() }

func (s *turnStream) Close() {
	s.stopOnce.Do(func() {
		close(s.stop)
		s.cancel()
	})
}

func (s *turnStream) AnswerPermission(id string, response permissions.Response) error {
	return s.turn.AnswerPermission(id, response)
}

func waitStream(ch <-chan agent.TurnEvent) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-ch
		if !ok {
			return nil
		}
		batch := []agent.TurnEvent{event}
		for {
			select {
			case event, ok := <-ch:
				if !ok {
					return streamBatchMsg(batch)
				}
				batch = append(batch, event)
			default:
				return streamBatchMsg(batch)
			}
		}
	}
}

func (m *Model) startStream(text string) (Model, tea.Cmd) {
	if err := m.opts.Conv.AddUserMessage(text); err != nil {
		m.transcript.Emit(block{kind: blockNotice, body: "Cannot send message: " + err.Error(), severity: noticeError})
		return *m, nil
	}
	m.transcript.Emit(block{kind: blockUser, body: text})
	return m.startTurn()
}

func (m *Model) startTurn() (Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	turn := m.opts.Agent.StartTurn(ctx, m.opts.Conv)
	m.turn = startTurnStream(turn, cancel)
	m.composer.Reset()
	m.scroll = scrollState{follow: true}
	m.live.Begin()
	return *m, tea.Sequence(m.clock.Start(), waitStream(m.turn.events))
}

func (m *Model) handleStreamBatch(batch streamBatchMsg) (Model, tea.Cmd) {
	var cmds []tea.Cmd
	for _, event := range batch {
		if cmd := m.foldEvent(event); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if m.turn == nil {
			break
		}
	}
	if m.turn != nil {
		cmds = append(cmds, waitStream(m.turn.events))
	}
	return *m, tea.Batch(cmds...)
}

func (m *Model) foldEvent(event agent.TurnEvent) tea.Cmd {
	switch e := event.(type) {
	case agent.TextDelta:
		m.live.AppendText(e.Text)
	case agent.TextComplete:
		if text := m.live.TakeText(); text != "" {
			m.transcript.Emit(block{kind: blockAssistant, body: text})
		}
	case agent.ThinkingDelta, agent.ThinkingCompleted:
		// Thinking is available to other frontends but has no TUI surface.
	case agent.ToolStarted:
		m.live.BeginTool(e.ToolID, toolTitle(m.opts.Agent.Registry(), e.ToolName, e.Args))
	case agent.ToolFinished:
		if title, ok := m.live.EndTool(e.ToolID); ok {
			m.transcript.Emit(block{
				kind:    blockTool,
				title:   title,
				body:    e.Result.Output,
				elapsed: e.Elapsed,
				isErr:   e.Result.IsError,
				isDiff:  e.Result.IsDiff,
			})
		}
	case agent.UsageUpdated:
		m.lastUsage = e.Usage
	case agent.CompactionProgress:
		body, severity := compactionNotice(e.Progress)
		m.transcript.Emit(block{kind: blockNotice, body: body, severity: severity})
	case agent.PermissionRequested:
		m.dialog.Open(e.ID, &e.Prompt)
		m.live.Pause()
	case agent.TurnFailed, agent.TurnCompleted:
		m.closeLive(event)
	}
	return nil
}

func (m *Model) closeLive(event agent.TurnEvent) {
	if m.turn == nil {
		return
	}
	elapsed := m.live.Elapsed()
	if text := m.live.TakeText(); text != "" {
		m.transcript.Emit(block{kind: blockAssistant, body: text})
	}
	switch e := event.(type) {
	case agent.TurnCompleted:
		switch e.Reason {
		case agent.StoppedByModel:
			m.transcript.Emit(block{kind: blockTurn, elapsed: elapsed, at: time.Now(), usage: m.lastUsage})
		case agent.StoppedByLimit:
			limit := fmt.Sprintf("Round limit reached after %d tool rounds — the loop stopped before running out of control.", e.ToolRounds)
			m.transcript.Emit(block{kind: blockNotice, body: limit, severity: noticeWarn})
			m.transcript.Emit(block{kind: blockTurn, elapsed: elapsed, at: time.Now(), usage: m.lastUsage})
		case agent.StoppedByUser:
			m.transcript.Emit(block{kind: blockNotice, body: "Cancelled."})
		}
	case agent.TurnFailed:
		m.transcript.Emit(block{kind: blockNotice, body: "Error: " + e.Err.Error(), severity: noticeError})
	}
	m.answerPermission(permissions.Reject)
	m.live.Finish()
	m.turn.Close()
	m.turn = nil
	m.reportSessionError()
	m.stopClockIfIdle()
}

func (m *Model) answerPermission(response permissions.Response) {
	id, prompt := m.dialog.Close()
	if prompt == nil {
		return
	}
	m.live.Resume()
	if m.turn != nil {
		_ = m.turn.AnswerPermission(id, response)
	}
}

func (m *Model) cancelTurn() {
	if m.turn != nil {
		m.turn.Cancel()
	}
}

func (m *Model) reportSessionError() {
	if m.sessionWarned || m.opts.Session == nil {
		return
	}
	if err := m.opts.Session.Err(); err != nil {
		m.sessionWarned = true
		m.transcript.Emit(block{kind: blockNotice, body: "Session archive is not being saved: " + err.Error(), severity: noticeWarn})
	}
}

func compactionNotice(progress compaction.Progress) (string, noticeSeverity) {
	switch progress.Phase {
	case compaction.PhaseStart:
		return "Compacting conversation…", noticeInfo
	case compaction.PhaseDone:
		return fmt.Sprintf("Context compacted: %s → %s tokens",
			format.Tokens(progress.FromTokens), format.Tokens(progress.ToTokens)), noticeInfo
	case compaction.PhaseFailed:
		return "Compaction failed: " + progress.Err.Error(), noticeWarn
	default:
		return "", noticeInfo
	}
}

func runCompact(mgr Compactor) tea.Cmd {
	return func() tea.Msg {
		result, err := mgr.ForceCompact(context.Background(), nil)
		return compactDoneMsg{result: result, err: err}
	}
}

func (m Model) compactDone(msg compactDoneMsg) (Model, tea.Cmd) {
	m.compacting = false
	switch {
	case msg.err != nil:
		body, severity := compactionNotice(compaction.Progress{Phase: compaction.PhaseFailed, Err: msg.err})
		m.transcript.Emit(block{kind: blockNotice, body: body, severity: severity})
	case !msg.result.Compacted:
		m.transcript.Emit(block{kind: blockNotice, body: "Nothing to compact."})
	default:
		body, severity := compactionNotice(compaction.Progress{
			Phase:      compaction.PhaseDone,
			FromTokens: msg.result.FromTokens,
			ToTokens:   msg.result.ToTokens,
		})
		m.transcript.Emit(block{kind: blockNotice, body: body, severity: severity})
	}
	m.stopClockIfIdle()
	return m, nil
}
