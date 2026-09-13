// Package tui implements the interactive terminal interface: a startup
// banner, chat history printed to the terminal's main buffer, a live frame
// with the input box and status bar, per-block reply commit, spinner and
// slash commands.
//
// The program runs inline (no alternate screen). Committed content — user
// questions, settled replies, tool results, notices — is printed above the
// live frame via tea.Println and accumulates in the terminal's own
// scrollback, so wheel scrolling and text selection stay native. The live
// frame holds only in-flight state: running tool lines, the progress
// spinner, the input box and the status bar. It starts right below the
// banner and is pushed down as history prints, pinning to the bottom once
// the screen fills.
//
// Data flow is deliberately flat: one shared conversation history plus a
// single in-flight liveStream. Reply text is not streamed onto the screen —
// each text content block commits whole when the block ends (TextComplete),
// so while a turn runs the only animated thing is the spinner line.
package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/tools"
)

var (
	timeNow   = time.Now
	timeSince = time.Since
)

type Options struct {
	Agent        *agent.Orchestrator
	Conv         *conversation.Manager // shared with the agent: both sides see one history
	Registry     *tools.Registry       // queried for tool entry titles
	ProviderName string
	ModelName    string
	Version      string
	WorkDir      string
}

type Model struct {
	opts     Options
	conv     *conversation.Manager
	live     *liveStream // nil when idle
	running  []toolCall  // tool calls in flight, shown in the live frame
	input    textarea.Model
	spinner  spinner.Model
	width    int
	renderer *glamour.TermRenderer

	// print emits committed content above the live frame. Production wires
	// tea.Println; tests capture into a recorder instead. It must only be
	// called from Update paths: printing stays ordered against the event
	// stream via tea.Sequence (see startStream and handleStreamBatch).
	print func(string) tea.Cmd
	// bannerDone marks the opening block printed on the first window size.
	bannerDone bool
}

// toolCall is one in-flight tool call shown in the live frame between
// ToolCallBegin and ToolCallEnd. id is the model-issued call id: two calls
// to the same tool may be in flight at once, so name alone cannot pair
// Begin with End.
type toolCall struct {
	id    string
	title string
}

// toolTitle summarizes one tool call's arguments into a human title by
// asking the tool itself (TitledTool), mewcode style: Read a.go / Write a.go
// (12 lines) / Edit a.go / Bash: <command> / Glob: pattern. Tools that don't
// implement the interface — and unregistered names — fall back to the plain
// tool name.
func toolTitle(reg *tools.Registry, name string, args map[string]any) string {
	if reg != nil {
		if t := reg.Get(name); t != nil {
			if tt, ok := t.(tools.TitledTool); ok {
				if title := tt.Title(args); title != "" {
					return title
				}
			}
		}
	}
	return name
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

	in.MinHeight = 1
	in.MaxHeight = 8
	in.MaxWidth = 0
	in.DynamicHeight = true

	in.SetStyles(inputStyles())
	in.Focus()

	return Model{
		opts:    opts,
		conv:    opts.Conv,
		input:   in,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot)),
		print:   func(s string) tea.Cmd { return tea.Println(s) },
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return m.input.Focus()
}

// println emits committed content above the live frame through the wired
// print function (tea.Println in production). The trailing newline appended
// here is the one-blank-line separator every block keeps between itself and
// whatever prints next (or the live frame below). The nil guard keeps
// zero-value models from panicking.
func (m *Model) println(block string) tea.Cmd {
	if m.print == nil || block == "" {
		return nil
	}
	return m.print(block + "\n")
}

// Update implements tea.Model. It is the single entry point for every visible
// state change: size, keys, stream batches and ticks are all dispatched here.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.relayout()
		// The opening block prints once, on the first size report: only
		// then is the real width known, so narrow terminals get the
		// text-only banner variant.
		if !m.bannerDone {
			m.bannerDone = true
			return m, m.println(renderStartup(m.opts.Version, m.opts.WorkDir, m.width))
		}
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m.quit()
		case "esc":
			// Esc cancels the in-flight loop (LoopComplete{OutcomeCancelled}
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
		return m, cmd
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
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
		m.input.Reset()
		return m, m.println(renderClearedBlock())
	case cmdPlan:
		// Mode switches only take effect between loops: handleEnter's
		// early return above already ignored them while a loop is in flight.
		m.opts.Agent.SetMode(agent.ModePlan)
		m.input.Reset()
		return m, m.println(renderNoticeBlock(
			"Plan mode on: only read tools are offered — the model drafts a plan. Use /do to switch back for execution.", noticeInfo))
	case cmdDo:
		m.opts.Agent.SetMode(agent.ModeNormal)
		m.input.Reset()
		return m, m.println(renderNoticeBlock(
			"Plan mode off: all tools available. Send your execution instruction.", noticeInfo))
	case cmdUnknown:
		m.input.Reset()
		return m, m.println(renderNoticeBlock(hint, noticeError))
	}
	return m, m.startStream(text)
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
// loop. The question is printed to the history first; the agent owns the
// history commit for the whole turn. The user block is sequenced before
// waitStream so it can never lose a race against the first batch's blocks:
// Sequence enqueues the print message before the stream wait even starts.
func (m *Model) startStream(text string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	block := renderUserBlock(m, text)
	m.live = &liveStream{
		events: m.opts.Agent.RunLoop(ctx, text),
		ctx:    ctx,
		cancel: cancel,
		start:  timeNow(),
	}
	m.input.Reset()
	return tea.Sequence(m.println(block),
		tea.Batch(waitStream(m.live.events), m.spinner.Tick))
}

// handleStreamBatch folds a coalesced batch of events, printing the blocks it
// committed and re-arming the wait. Blocks queued here are sequenced before
// the next waitStream for the same reason as startStream: the print message
// is enqueued before the next batch can be produced, so printed history
// never reorders under streaming bursts. It returns a Model value so Update's
// dynamic type stays stable: every arm of Update yields exactly Model, never
// *Model.
func (m *Model) handleStreamBatch(batch streamBatchMsg) (Model, tea.Cmd) {
	var out []string
	for _, ev := range batch {
		out = append(out, m.foldEvent(ev)...)
		if m.live == nil { // a terminal event consumed; drop remaining events
			break
		}
	}
	var cmd tea.Cmd
	if len(out) > 0 {
		// One print per Update: blocks join into a single chunk with a blank
		// line between them, so a burst of commits lands as one ordered slab.
		cmd = m.println(strings.Join(out, "\n\n"))
	}
	if m.live != nil {
		return *m, tea.Sequence(cmd, waitStream(m.live.events))
	}
	return *m, cmd
}

// foldEvent folds one agent event and returns the blocks to print. Text
// deltas accumulate on the live stream; tool calls move between the live
// frame and the printed history; terminal events close the loop.
func (m *Model) foldEvent(ev agent.AgentEvent) []string {
	switch e := ev.(type) {
	case agent.TextDelta:
		if m.live != nil {
			m.live.text.WriteString(e.Text)
		}
	case agent.TextComplete:
		return m.flushText()
	case agent.ToolCallBegin:
		m.running = append(m.running, toolCall{
			id:    e.ToolID,
			title: toolTitle(m.opts.Registry, e.ToolName, e.Args),
		})
	case agent.ToolCallEnd:
		for i := range m.running {
			if m.running[i].id != e.ToolID {
				continue
			}
			tc := m.running[i]
			m.running = slices.Delete(m.running, i, i+1)
			return []string{renderToolDoneBlock(tc.title, e.Result.IsError, e.Elapsed, diffText(e.Result))}
		}
		// No matching Begin: a stray End after cancel; nothing to settle.
	case agent.TurnComplete:
		if m.live != nil {
			m.live.turn = e.Turn
		}
	case agent.LoopError, agent.LoopComplete:
		return m.closeLive(ev)
	default:
		// ThinkingDelta, ThinkingComplete: not surfaced this phase.
	}
	return nil
}

// diffText returns the result's diff rendering when the tool marked its
// output as one (edit_file on success), empty otherwise. Errors are plain
// notices: a failed tool carries no diff.
func diffText(r tools.ToolResult) string {
	if r.Diff {
		return r.Output
	}
	return ""
}

// flushText commits the accumulated reply text as a printed assistant block.
func (m *Model) flushText() []string {
	if m.live == nil {
		return nil
	}
	if text := m.live.text.String(); text != "" {
		m.live.text.Reset()
		return []string{renderAssistantBlock(m, text)}
	}
	return nil
}

// closeLive closes the turn on a terminal event, flushing pending text first
// and printing the outcome notice and footer. Any tool calls still marked
// running die with the loop (their End events would only arrive as strays).
func (m *Model) closeLive(ev agent.AgentEvent) []string {
	ls := m.live
	if ls == nil {
		return nil
	}
	defer func() {
		ls.cancel()
		m.live = nil
		m.running = nil
	}()
	out := m.flushText()
	switch ev := ev.(type) {
	case agent.LoopComplete:
		switch ev.Outcome {
		case agent.OutcomeModelStop:
			out = append(out, renderTurnBlock(timeSince(ls.start), timeNow()))
		case agent.OutcomeMaxIterations:
			out = append(out, renderNoticeBlock(
				fmt.Sprintf("Turn limit reached after %d turn(s) — the loop stopped before running out of control.", ev.TotalTurns), noticeWarn))
			out = append(out, renderTurnBlock(timeSince(ls.start), timeNow()))
		case agent.OutcomeUnknownTool:
			out = append(out, renderNoticeBlock(
				"Stopped: the model called unknown tools repeatedly.", noticeWarn))
			out = append(out, renderTurnBlock(timeSince(ls.start), timeNow()))
		case agent.OutcomeCancelled:
			out = append(out, renderNoticeBlock("Cancelled.", noticeInfo))
		}
	case agent.LoopError:
		out = append(out, renderNoticeBlock("Error: "+ev.Err.Error(), noticeError))
	}
	return out
}

// relayout rebuilds width-dependent state: the input's width and the markdown
// renderer's wrap width. Already-printed history keeps its original wrapping —
// it lives in the terminal's buffer and cannot be re-flowed.
func (m *Model) relayout() {
	if m.width <= 0 {
		return
	}

	m.input.SetWidth(max(m.width-6, 10))

	wrap := max(m.width-2, 20)

	sc := styles.DarkStyleConfig
	var zeroMargin uint
	sc.Document.Margin = &zeroMargin
	sc.Document.BlockPrefix = ""
	sc.Document.BlockSuffix = ""
	if r, err := glamour.NewTermRenderer(glamour.WithStyles(sc), glamour.WithWordWrap(wrap)); err == nil {
		m.renderer = r
	}
}
