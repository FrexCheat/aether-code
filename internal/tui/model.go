// Package tui implements the interactive terminal interface: banner, chat
// viewport, input box, status bar, per-block reply commit, spinner and slash
// commands.
//
// Data flow is deliberately flat: a committed []entry history plus a single
// in-flight liveStream. Reply text is not streamed onto the screen — each
// text content block commits whole when the block ends (TextComplete), so
// while a turn runs the only animated thing is the bubbles spinner line.
package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/conversation"
)

// Options configures the TUI.
type Options struct {
	Agent        *agent.Orchestrator
	Conv         *conversation.Manager // shared with the agent: both sides see one history
	ProviderName string
	ModelName    string
	Version      string
	WorkDir      string
}

// Model is the Bubble Tea model for the chat UI. It is a thin controller: the
// committed history lives in entry.go, stream plumbing in streaming.go, and
// rendering in render.go — every visible state change enters through Update.
type Model struct {
	opts Options
	conv *conversation.Manager

	entries []entry
	live    *liveStream // nil when idle

	viewport viewport.Model
	input    textarea.Model
	spinner  spinner.Model
	width    int
	height   int
	renderer *glamour.TermRenderer
}

// Run starts the program and blocks until it exits.
func Run(opts Options) error {
	_, err := tea.NewProgram(newModel(opts)).Run()
	return err
}

func newModel(opts Options) Model {
	in := textarea.New()
	in.Prompt = ""
	in.Placeholder = "Send a message..."
	in.ShowLineNumbers = false
	in.CharLimit = 0
	// The input grows with its content up to MaxHeight lines and settles back
	// to a single line when cleared, instead of a fixed-size box. Width is
	// set on each relayout; MaxWidth stays 0 so a wide terminal never clips
	// the box at the component's default 500 columns.
	in.MinHeight = 1
	in.MaxHeight = 8
	in.MaxWidth = 0
	in.DynamicHeight = true
	// The default dark styles paint the focused line black; see inputStyles.
	in.SetStyles(inputStyles())
	in.Focus() // set focus on the instance that is stored in Model (Init would only touch a copy)

	vp := viewport.New()
	vp.KeyMap = viewport.KeyMap{
		PageDown: key.NewBinding(key.WithKeys("pgdown")),
		PageUp:   key.NewBinding(key.WithKeys("pgup")),
	}
	vp.MouseWheelEnabled = true

	return Model{
		opts:     opts,
		conv:     opts.Conv,
		viewport: vp,
		input:    in,
		spinner:  spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return m.input.Focus()
}

// refresh pushes the new frame into the viewport, following the bottom only
// when the user is already there. It runs inside Update (where Model is
// addressable), never in View: bubbletea renders from a snapshot and any
// viewport motion done in View would be lost before the next frame.
func (m *Model) refresh() {
	atBottom := m.viewport.AtBottom()
	// The input grows/shrinks as its content does, so the chat area has to be
	// re-derived from it before the frame is laid out.
	m.syncViewportHeight()
	m.viewport.SetContent(renderTop(m))
	if atBottom {
		m.viewport.GotoBottom()
	}
}

// syncViewportHeight resizes the chat viewport to whatever is left above the
// input box and status bar, so the stacked regions always add up to the window
// height and the input never pushes the status bar off screen.
func (m *Model) syncViewportHeight() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	chatH := m.height - inputViewHeight(m) - statusBarHeight
	if chatH < 1 {
		chatH = 1
	}
	m.viewport.SetHeight(chatH)
}

// inputViewHeight is the rendered height of the input box: the textarea's
// content plus the rounded border's top and bottom. styleInputBox has no
// vertical padding, so it is simply content + 2.
func inputViewHeight(m *Model) int {
	return m.input.Height() + 2
}

// Update implements tea.Model. It is the single entry point for every visible
// state change: size, keys, stream batches and ticks are all dispatched here,
// and arms that change content end with a refresh so the viewport never goes
// stale.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.relayout()
		m.refresh()
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m.quit()
		case "esc":
			// Esc cancels the in-flight loop (the terminal Cancelled event
			// then closes it); when idle it does nothing. ctrl+c still quits.
			if m.live != nil {
				m.live.cancel()
			}
			return m, nil
		case "enter":
			return m.handleEnter()
		}

	case streamBatchMsg:
		return m.handleStreamBatch(msg)

	case spinner.TickMsg:
		// The spinner's own tick chain drives the live frames. While idle the
		// chain is allowed to die (no animation to run); each turn restarts it
		// with m.spinner.Tick in startStream.
		if m.live == nil {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		m.refresh()
		return m, cmd
	}

	var cmds []tea.Cmd
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	// Typing/pasting changes the input's height; refresh re-sizes the viewport
	// to the leftover space and keeps it pinned to the bottom when it was.
	m.refresh()
	return m, tea.Batch(cmds...)
}

func (m Model) quit() (Model, tea.Cmd) {
	if m.live != nil {
		m.live.cancel()
	}
	return m, tea.Quit
}

func (m Model) handleEnter() (Model, tea.Cmd) {
	if m.live != nil {
		return m, nil // input is ignored while a turn is in flight
	}
	// Shell-style line continuation: "<text>\" + Enter starts a new line
	// instead of submitting. The backslash is dropped and a newline takes its
	// place, so multi-line prompts can be typed like in a terminal.
	if lineContinues(m.input) {
		m.input.SetValue(continueValue(m.input.Value()))
		m.refresh()
		return m, nil
	}
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	cmd, hint := parseCommand(text)
	switch cmd {
	case cmdQuit:
		return m.quit()
	case cmdClear:
		m.conv.Clear()
		m.entries = nil
		m.input.Reset()
		m.refresh()
		return m, nil
	case cmdPlan:
		// Mode switches only take effect between loops: handleEnter's
		// early return above already ignored them while a loop is in flight.
		m.opts.Agent.SetMode(agent.ModePlan)
		m.entries = append(m.entries, newNoticeEntry(
			"Plan mode on: only read tools are offered — the model drafts a plan. Use /do to switch back for execution.", noticeInfo))
		m.input.Reset()
		m.refresh()
		return m, nil
	case cmdDo:
		m.opts.Agent.SetMode(agent.ModeNormal)
		m.entries = append(m.entries, newNoticeEntry(
			"Plan mode off: all tools available. Send your execution instruction.", noticeInfo))
		m.input.Reset()
		m.refresh()
		return m, nil
	case cmdUnknown:
		m.entries = append(m.entries, newNoticeEntry(hint, noticeError))
		m.input.Reset()
		m.refresh()
		return m, nil
	}
	c := m.startStream(text)
	m.refresh()
	return m, c
}

// lineContinues reports whether an Enter press should start a new line instead
// of submitting: the input ends with a lone backslash (trailing spaces/tabs
// allowed) and the cursor sits at the very end of the last line.
func lineContinues(in textarea.Model) bool {
	value := in.Value()
	if !strings.HasSuffix(strings.TrimRight(value, " \t"), `\`) {
		return false
	}
	lines := strings.Split(value, "\n")
	return in.Line() == len(lines)-1 && in.Column() == len([]rune(lines[len(lines)-1]))
}

// continueValue turns a continuation buffer ("…foo \" or "…foo \  ") into the
// next line of the prompt: trailing whitespace and the backslash are removed
// and a newline is inserted in their place.
func continueValue(value string) string {
	return strings.TrimSuffix(strings.TrimRight(value, " \t"), `\`) + "\n"
}

// startStream opens a new turn and wires the agent's event stream into the
// loop. The question is appended as a user entry and held on the liveStream;
// the agent owns the history commit for the whole turn.
func (m *Model) startStream(text string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.entries = append(m.entries, newUserEntry(text))
	m.live = &liveStream{
		events:   m.opts.Agent.RunLoop(ctx, text),
		ctx:      ctx,
		cancel:   cancel,
		question: text,
		start:    timeNow(),
	}
	m.input.Reset()
	return tea.Batch(waitStream(m.live.events), m.spinner.Tick)
}

// handleStreamBatch folds a coalesced batch of events into the live turn and
// commits the reply whole on a terminal event. It returns a Model value so
// Update's dynamic type stays stable: every arm of Update yields exactly Model,
// never *Model.
func (m *Model) handleStreamBatch(batch streamBatchMsg) (Model, tea.Cmd) {
	for _, ev := range batch {
		m.foldEvent(ev)
		if m.live == nil { // a terminal event consumed; drop remaining events
			break
		}
	}
	if m.live != nil {
		return *m, waitStream(m.live.events)
	}
	m.refresh()
	return *m, nil
}

// foldEvent folds one agent event. Text deltas accumulate on the live stream;
// tool events open and settle tool entries; terminal events close the loop.
func (m *Model) foldEvent(ev agent.AgentEvent) {
	switch e := ev.(type) {
	case agent.TextDelta:
		if m.live != nil {
			m.live.text.WriteString(e.Text)
		}
	case agent.TextComplete:
		// One text content block finished: commit it as its own assistant
		// entry so a segment spoken before a tool call stays above that call.
		m.flushText()
	case agent.ToolCallBegin:
		m.entries = append(m.entries, newToolEntry(e.ToolID, e.ToolName, toolTitle(e.ToolName, e.Args)))
	case agent.ToolCallEnd:
		// Settle the running entry with this call's id. Ids are unique per
		// call, so same-name concurrent calls still pair correctly; results
		// arrive in the model's original call order.
		for i := len(m.entries) - 1; i >= 0; i-- {
			en := &m.entries[i]
			if en.kind == entryTool && en.toolID == e.ToolID && en.state == toolRunning {
				en.settleTool(e.Result.IsError, e.Elapsed, editDiff(e))
				break
			}
		}
	case agent.TurnComplete:
		// One tool round finished and the next request is going out; the
		// spinner line shows which turn is running.
		if m.live != nil {
			m.live.turn = e.Turn
		}
	case agent.TurnError, agent.LoopComplete:
		m.closeLive(ev)
	default:
		// ThinkingDelta, ThinkingComplete: not surfaced this phase.
	}
}

// editDiff returns the diff text for edit_file results, empty for other tools.
func editDiff(e agent.ToolCallEnd) string {
	if e.ToolName != "edit_file" {
		return ""
	}
	return e.Result.Output
}

// flushText commits the live stream's accumulated reply text as one assistant
// entry and clears the buffer. It fires when a text content block ends
// (TextComplete), as a provider fallback on ToolCallBegin, and at turn end
// for whatever is left. Empty buffers produce no entry.
func (m *Model) flushText() {
	if m.live == nil {
		return
	}
	if text := m.live.text.String(); text != "" {
		m.entries = append(m.entries, newAssistantEntry(text))
		m.live.text.Reset()
	}
}

// closeLive settles the in-flight loop: remaining text flushes, then the
// terminal event grades the closing entries. LoopComplete tiers by outcome —
// ModelStop gets the done footer, MaxIterations/UnknownTool get a yellow
// guard-rail notice plus the footer, Cancelled a neutral notice. TurnError
// appends an error notice. History commits already happened inside the agent
// — this only touches the display entries.
func (m *Model) closeLive(ev agent.AgentEvent) {
	ls := m.live
	if ls == nil {
		return
	}
	defer func() {
		ls.cancel()
		m.live = nil
	}()
	m.flushText()
	switch ev := ev.(type) {
	case agent.LoopComplete:
		switch ev.Outcome {
		case agent.OutcomeModelStop:
			m.entries = append(m.entries, newTurnEntry(timeSince(ls.start), timeNow()))
		case agent.OutcomeMaxIterations:
			m.entries = append(m.entries, newNoticeEntry(
				fmt.Sprintf("Turn limit reached after %d turn(s) — the loop stopped before running out of control.", ev.TotalTurns), noticeWarn))
			m.entries = append(m.entries, newTurnEntry(timeSince(ls.start), timeNow()))
		case agent.OutcomeUnknownTool:
			m.entries = append(m.entries, newNoticeEntry(
				"Stopped: the model called unknown tools repeatedly.", noticeWarn))
			m.entries = append(m.entries, newTurnEntry(timeSince(ls.start), timeNow()))
		case agent.OutcomeCancelled:
			m.entries = append(m.entries, newNoticeEntry("Cancelled.", noticeInfo))
		}
	case agent.TurnError:
		m.entries = append(m.entries, newNoticeEntry("Error: "+ev.Err.Error(), noticeError))
	}
}

// relayout distributes the terminal height between the bottom region and the
// chat viewport, sizes the multiline input, and rebuilds the markdown renderer
// for the new width.
func (m *Model) relayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	m.viewport.SetWidth(m.width)
	// SetWidth re-wraps the input; with DynamicHeight that also recomputes how
	// tall it wants to be. The input's height is never set directly — it grows
	// and shrinks with the content.
	m.input.SetWidth(max(m.width-6, 10))
	m.syncViewportHeight()

	wrap := max(m.width-2, 20)
	// Start from the dark style but remove the document margin and the
	// surrounding blank lines so rendered replies sit flush with their role
	// label and keep turn spacing consistent.
	sc := styles.DarkStyleConfig
	var zeroMargin uint
	sc.Document.Margin = &zeroMargin
	sc.Document.BlockPrefix = ""
	sc.Document.BlockSuffix = ""
	if r, err := glamour.NewTermRenderer(glamour.WithStyles(sc), glamour.WithWordWrap(wrap)); err == nil {
		m.renderer = r
	}
}
