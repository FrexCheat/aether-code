package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/compaction"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/permissions"
)

type streamBatchMsg []agent.Event

type compactDoneMsg struct {
	result compaction.Result
	err    error
}

// waitStream drains whatever the event channel already holds into one batch, so
// a delta burst renders as a single frame.
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

func (m *Model) startStream(text string) (Model, tea.Cmd) {
	if err := m.opts.Conv.AddUserMessage(text); err != nil {
		// The input keeps its text so the user can retry once the archive
		// recovers.
		m.emit(block{kind: blockNotice, body: "Cannot send message: " + err.Error(), severity: noticeError})
		return *m, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.composer.Reset()
	m.emit(block{kind: blockUser, body: text})
	m.scroll = scrollState{follow: true}
	m.live.Begin(m.opts.Agent.Run(ctx, m.opts.Conv), cancel)
	return *m, tea.Sequence(m.clock.Start(), waitStream(m.live.Events()))
}

func (m *Model) handleStreamBatch(batch streamBatchMsg) (Model, tea.Cmd) {
	var cmds []tea.Cmd
	for _, ev := range batch {
		if cmd := m.foldEvent(ev); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if !m.live.Active() {
			break
		}
	}
	if m.live.Active() {
		cmds = append(cmds, waitStream(m.live.Events()))
	}
	return *m, tea.Batch(cmds...)
}

func (m *Model) foldEvent(ev agent.Event) tea.Cmd {
	switch e := ev.(type) {
	case agent.TextDelta:
		m.live.AppendText(e.Text)
	case agent.TextComplete:
		if text := m.live.TakeText(); text != "" {
			m.emit(block{kind: blockAssistant, body: text})
		}
	case agent.ToolCallBegin:
		m.live.BeginTool(e.ToolID, toolTitle(m.opts.Agent.Registry(), e.ToolName, e.Args))
	case agent.ToolCallEnd:
		if title, ok := m.live.EndTool(e.ToolID); ok {
			m.emit(block{
				kind:    blockTool,
				title:   title,
				body:    e.Result.Output,
				elapsed: e.Elapsed,
				isErr:   e.Result.IsError,
				isDiff:  e.Result.IsDiff,
			})
		}
	case agent.CompactionProgress:
		body, severity := compactionNotice(e.Progress)
		m.emit(block{kind: blockNotice, body: body, severity: severity})
	case agent.PermissionRequestEvent:
		m.dialog.Open(&e.Request)
		m.live.Pause()
	case agent.LoopError, agent.LoopComplete:
		m.closeLive(ev)
	}
	return nil
}

func (m *Model) closeLive(ev agent.Event) {
	if !m.live.Active() {
		return
	}
	elapsed := m.live.Elapsed()
	if text := m.live.TakeText(); text != "" {
		m.emit(block{kind: blockAssistant, body: text})
	}
	switch e := ev.(type) {
	case agent.LoopComplete:
		if e.Usage != (llm.Usage{}) {
			m.lastUsage = e.Usage
		}
		switch e.Reason {
		case agent.StoppedByModel:
			m.emit(block{kind: blockTurn, elapsed: elapsed, at: time.Now(), usage: e.Usage})
		case agent.StoppedByLimit:
			limit := fmt.Sprintf("Round limit reached after %d tool rounds — the loop stopped before running out of control.", e.ToolRounds)
			m.emit(block{kind: blockNotice, body: limit, severity: noticeWarn})
			m.emit(block{kind: blockTurn, elapsed: elapsed, at: time.Now(), usage: e.Usage})
		case agent.StoppedByUser:
			m.emit(block{kind: blockNotice, body: "Cancelled."})
		}
	case agent.LoopError:
		m.emit(block{kind: blockNotice, body: "Error: " + e.Err.Error(), severity: noticeError})
	}
	m.answerPermission(permissions.Reject)
	m.live.Cancel()
	m.live.Finish()
	m.reportSessionError()
	m.stopClockIfIdle()
}

// reportSessionError surfaces the archive's first write failure once; the
// inline paths (a refused message, a failed turn) already told the user, and
// this latch covers the silent ones.
func (m *Model) reportSessionError() {
	if m.sessionWarned || m.session == nil {
		return
	}
	if err := m.session.Err(); err != nil {
		m.sessionWarned = true
		m.emit(block{kind: blockNotice, body: "Session archive is not being saved: " + err.Error(), severity: noticeWarn})
	}
}

// emit records a block and renders it into the history.
func (m *Model) emit(b block) {
	m.blocks = append(m.blocks, b)
	m.history = append(m.history, m.blockLines(b)...)
}

// blockLines renders one block for the history, followed by a blank separator.
func (m Model) blockLines(b block) []string {
	lines := hardWrap(m.renderer.Render(b, false), m.width)
	return append(lines, "")
}

// rebuildHistory re-renders the banner and every recorded block at the current
// width.
func (m *Model) rebuildHistory() {
	m.history = make([]string, 0, len(m.history)+1)
	if m.width > 0 {
		banner := m.renderer.Banner(m.opts.Version, m.opts.WorkDir, m.opts.Provider.Model, m.tips)
		m.history = append(m.history, hardWrap(banner, m.width)...)
		m.history = append(m.history, "")
	}
	for _, b := range m.blocks {
		m.history = append(m.history, m.blockLines(b)...)
	}
}

// compactionNotice renders one compaction progress step; both the in-turn
// path and /compact flow through it so the wording cannot diverge.
func compactionNotice(p compaction.Progress) (string, noticeSeverity) {
	switch p.Phase {
	case compaction.PhaseStart:
		return "Compacting conversation…", noticeInfo
	case compaction.PhaseDone:
		return fmt.Sprintf("Context compacted: %s → %s tokens",
			formatTokenCount(p.FromTokens), formatTokenCount(p.ToTokens)), noticeInfo
	case compaction.PhaseFailed:
		return "Compaction failed: " + p.Err.Error(), noticeWarn
	default:
		return "", noticeInfo
	}
}

// runCompact is the only writer of the history outside a turn; it runs while
// the input gate is closed.
func runCompact(mgr Compactor) tea.Cmd {
	return func() tea.Msg {
		res, err := mgr.ForceCompact(context.Background(), nil)
		return compactDoneMsg{result: res, err: err}
	}
}

func (m Model) compactDone(msg compactDoneMsg) (Model, tea.Cmd) {
	m.compacting = false
	switch {
	case msg.err != nil:
		body, severity := compactionNotice(compaction.Progress{Phase: compaction.PhaseFailed, Err: msg.err})
		m.emit(block{kind: blockNotice, body: body, severity: severity})
	case !msg.result.Compacted:
		m.emit(block{kind: blockNotice, body: "Nothing to compact."})
	default:
		body, severity := compactionNotice(compaction.Progress{Phase: compaction.PhaseDone,
			FromTokens: msg.result.FromTokens, ToTokens: msg.result.ToTokens})
		m.emit(block{kind: blockNotice, body: body, severity: severity})
	}
	m.stopClockIfIdle()
	return m, nil
}
