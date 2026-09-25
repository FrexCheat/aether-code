package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/compaction"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/memory"
	"github.com/FrexCheat/aether-code/internal/permissions"
)

type streamBatchMsg []agent.Event

type compactDoneMsg struct {
	result compaction.Result
	err    error
}

// extractTail is how many archived messages a resumed session re-offers to
// the extractor: a crashed tail then still gets one pass, and the overlap is
// cheap next to the dedup the extractor's prompt already asks for.
const extractTail = 40

type extractDoneMsg struct {
	sessionID string
	end       int
	applied   int
	err       error
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
	m.input.Reset()
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
	case agent.CompactionStart:
		m.emit(block{kind: blockNotice, body: "Compacting conversation…"})
	case agent.CompactionDone:
		m.emit(block{kind: blockNotice, body: fmt.Sprintf("Context compacted: %s → %s tokens",
			formatTokenCount(e.FromTokens), formatTokenCount(e.ToTokens))})
	case agent.CompactionFailed:
		m.emit(block{kind: blockNotice, body: "Compaction failed: " + e.Err.Error(), severity: noticeWarn})
	case agent.PermissionRequestEvent:
		m.dialog.Open(&e.Request)
		m.live.Pause()
	case agent.LoopError, agent.LoopComplete:
		return m.closeLive(ev)
	}
	return nil
}

func (m *Model) closeLive(ev agent.Event) tea.Cmd {
	if !m.live.Active() {
		return nil
	}
	elapsed := m.live.Elapsed()
	if text := m.live.TakeText(); text != "" {
		m.emit(block{kind: blockAssistant, body: text})
	}
	var extraction tea.Cmd
	switch e := ev.(type) {
	case agent.LoopComplete:
		if e.Usage != (llm.Usage{}) {
			m.lastUsage = e.Usage
		}
		switch e.Reason {
		case agent.StoppedByModel:
			m.emit(block{kind: blockTurn, elapsed: elapsed, at: time.Now(), usage: e.Usage})
			// The model finished on its own: the one moment a turn is worth
			// mining for memory notes.
			extraction = m.startExtraction()
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
	return extraction
}

// startExtraction runs one background note-extraction pass over the archived
// messages the previous pass has not seen; a tiny window or an in-flight pass
// is skipped, leaving the cursor where it is.
func (m *Model) startExtraction() tea.Cmd {
	if m.opts.Memory == nil || m.session == nil || m.extracting {
		return nil
	}
	msgs := m.session.MessagesFrom(m.extractCursor)
	if contentChars(msgs) < memory.MinExtractChars {
		return nil
	}
	m.extracting = true
	end := m.session.Count()
	sessionID := m.session.ID()
	mem := m.opts.Memory
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res, err := mem.Extract(ctx, msgs)
		return extractDoneMsg{sessionID: sessionID, end: end, applied: len(res.Applied), err: err}
	}
}

// extractDone settles one background pass; results for a session that is no
// longer current (a /clear happened meanwhile) are dropped.
func (m Model) extractDone(msg extractDoneMsg) (Model, tea.Cmd) {
	if m.session == nil || msg.sessionID != m.session.ID() {
		return m, nil
	}
	m.extracting = false
	if msg.err != nil {
		return m, nil // best-effort background work stays quiet
	}
	m.extractCursor = min(msg.end, m.session.Count())
	if msg.applied > 0 {
		m.emit(block{kind: blockNotice, body: fmt.Sprintf("Memory: %d note(s) saved", msg.applied)})
	}
	return m, nil
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

// contentChars measures a window cheaply, for the skip guard only.
func contentChars(msgs []conversation.Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
		for _, tr := range m.ToolResults {
			n += len(tr.Content)
		}
	}
	return n
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
		banner := m.renderer.Banner(m.opts.Version, m.opts.WorkDir, m.opts.Provider.Model)
		m.history = append(m.history, hardWrap(banner, m.width)...)
		m.history = append(m.history, "")
	}
	for _, b := range m.blocks {
		m.history = append(m.history, m.blockLines(b)...)
	}
}

// runCompact is the only writer of the history outside a turn; it runs while
// the input gate is closed.
func runCompact(mgr *compaction.Manager) tea.Cmd {
	return func() tea.Msg {
		res, err := mgr.ForceCompact(context.Background(), nil)
		return compactDoneMsg{result: res, err: err}
	}
}

func (m Model) compactDone(msg compactDoneMsg) (Model, tea.Cmd) {
	m.compacting = false
	switch {
	case msg.err != nil:
		m.emit(block{kind: blockNotice, body: "Compaction failed: " + msg.err.Error(), severity: noticeError})
	case !msg.result.Compacted:
		m.emit(block{kind: blockNotice, body: "Nothing to compact."})
	default:
		text := fmt.Sprintf("Context compacted: %s → %s tokens",
			formatTokenCount(msg.result.FromTokens), formatTokenCount(msg.result.ToTokens))
		m.emit(block{kind: blockNotice, body: text})
	}
	m.stopClockIfIdle()
	return m, nil
}
