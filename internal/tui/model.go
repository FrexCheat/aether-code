// Package tui implements the interactive terminal interface: banner, chat
// viewport, input box, status bar, whole-turn streaming commit, spinner and
// slash commands.
//
// Data flow is deliberately flat: a committed []entry history plus a single
// in-flight liveStream. Reply text is not streamed onto the screen — it is
// committed whole when the turn reaches a terminal event, so the only thing
// that changes during a turn is the animated spinner/timer line.
package tui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/prompt"
)

// Options configures the TUI.
type Options struct {
	Provider     llm.Client
	ProviderName string
	ModelName    string
	Thinking     bool
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
		conv:     conversation.NewManager(),
		viewport: vp,
		input:    in,
		spinner:  spinner.New(spinner.WithSpinner(spinner.Points)),
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
		case "enter":
			return m.handleEnter()
		}

	case streamBatchMsg:
		return m.handleStreamBatch(msg)

	case tickMsg:
		if m.live != nil {
			m.spinner, _ = m.spinner.Update(m.spinner.Tick())
			m.refresh()
			return m, tick()
		}
		return m, nil
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
	case cmdUnknown:
		m.entries = append(m.entries, newNoticeEntry(hint, true))
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

// startStream opens a new turn and wires the provider stream into the loop.
// The question is appended as a user entry and held on the liveStream for the
// history commit; there is no side-channel copy of it.
func (m *Model) startStream(text string) tea.Cmd {
	msgs := append(m.conv.GetMessages(), conversation.Message{Role: conversation.RoleUser, Content: text})
	req := llm.Request{
		System:   prompt.System,
		Messages: msgs,
		Thinking: m.opts.Thinking,
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.entries = append(m.entries, newUserEntry(text))
	m.live = &liveStream{
		events:   m.opts.Provider.Stream(ctx, req),
		cancel:   cancel,
		question: text,
		start:    timeNow(),
	}
	m.input.Reset()
	return tea.Batch(waitStream(m.live.events), tick())
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

// foldEvent folds one stream event. Text deltas accumulate on the live
// stream; terminal events commit the reply whole. Thinking and tool events
// are received but stored nowhere this phase — only the assistant text output
// is surfaced (spec "不做的事").
func (m *Model) foldEvent(ev llm.StreamEvent) {
	switch e := ev.(type) {
	case llm.TextDelta:
		if m.live != nil {
			m.live.text.WriteString(e.Text)
		}
	case llm.StreamEnd, llm.StreamError:
		m.closeLive(ev)
	default:
		// ThinkingDelta, ThinkingComplete, ToolCallStart/D/Complete: ignored.
	}
}

// closeLive settles the in-flight turn: StreamEnd commits question + text to
// history and appends the assistant entry; StreamError only appends an error
// notice and leaves history untouched. This is the only place that writes to
// the conversation history.
func (m *Model) closeLive(ev llm.StreamEvent) {
	ls := m.live
	if ls == nil {
		return
	}
	defer func() {
		ls.cancel()
		m.live = nil
	}()
	switch ev := ev.(type) {
	case llm.StreamEnd:
		text := ls.text.String()
		m.entries = append(m.entries, newAssistantEntry(text, timeSince(ls.start), timeNow()))
		m.conv.AddUserMessage(ls.question)
		m.conv.AddAssistantMessage(text)
	case llm.StreamError:
		m.entries = append(m.entries, newNoticeEntry("Error: "+ev.Err.Error(), true))
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
