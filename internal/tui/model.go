// Package tui implements the interactive terminal interface: banner, chat
// area, input box, status bar, streaming consumption and slash commands.
package tui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
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
// transcript state lives in turn.go, stream plumbing in streaming.go, and
// rendering in render.go — every visible state change enters through Update.
type Model struct {
	opts Options
	conv *conversation.Manager

	transcript Transcript
	live       *liveStream // nil when idle

	viewport viewport.Model
	input    textinput.Model
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
	in := textinput.New()
	in.Prompt = "❯ "
	in.Placeholder = "Send a message..."
	in.Focus()

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
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return m.input.Focus()
}

// refresh pushes the new frame into the viewport, following the bottom only
// when the user is already there. It must run inside Update (where Model is
// addressable), never in View: bubbletea renders from a snapshot and any
// viewport motion done in View would be lost before the next frame. It is the
// only place that writes rendered content into the viewport, and it always
// runs right after a state change.
func (m *Model) refresh() {
	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(renderTranscript(m))
	if atBottom {
		m.viewport.GotoBottom()
	}
}

// Update implements tea.Model. It is the single entry point for every visible
// state change: size, keys, stream batches and ticks are all dispatched here,
// and every arm ends with a refresh so the viewport never goes stale.
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
			return m.refreshAfter(m.handleEnter())
		}

	case streamBatchMsg:
		return m.refreshAfter(m.handleStreamBatch(msg))

	case tickMsg:
		if m.live != nil {
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
	return m, tea.Batch(cmds...)
}

// refreshAfter runs refresh after a handler that returns a new Model value.
func (m Model) refreshAfter(mm tea.Model, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	if nm, ok := mm.(Model); ok {
		nm.refresh()
		return nm, cmd
	}
	return mm, cmd
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
		m.transcript.Reset()
		m.input.Reset()
		m.refresh()
		return m, nil
	case cmdUnknown:
		m.transcript.Append(&Notice{Text: hint, Severity: SeverityError})
		m.input.Reset()
		m.refresh()
		return m, nil
	}
	c := m.startStream(text)
	m.refresh()
	return m, c
}

// startStream opens a new turn and wires the provider stream into the loop.
// The question lives on the Turn; there is no side-channel copy of it.
func (m *Model) startStream(text string) tea.Cmd {
	idx := openTurn(&m.transcript, text)
	turn, _ := m.transcript.At(idx).(*Turn)

	msgs := append(m.conv.GetMessages(), conversation.Message{Role: conversation.RoleUser, Content: turn.Question})
	req := llm.Request{
		System:   prompt.System,
		Messages: msgs,
		Thinking: m.opts.Thinking,
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.live = &liveStream{events: m.opts.Provider.Stream(ctx, req), cancel: cancel}
	m.input.Reset()
	return tea.Batch(waitStream(m.live.events), tick())
}

// handleStreamBatch folds a coalesced batch of events into the live turn.
// It returns a Model value so Update's dynamic type stays stable: every arm
// of Update yields exactly Model, never *Model.
func (m *Model) handleStreamBatch(batch streamBatchMsg) (Model, tea.Cmd) {
	idx := m.transcript.Len() - 1 // the live turn is always the last item
	for _, ev := range batch {
		applyEvent(&m.transcript, idx, ev, m.conv)
	}
	if turn, ok := m.transcript.At(idx).(*Turn); ok && turn.Status != TurnStreaming {
		if m.live != nil {
			m.live.cancel() // terminal event: release the stream context
			m.live = nil
		}
		return *m, nil
	}
	if m.live != nil {
		return *m, waitStream(m.live.events)
	}
	return *m, nil
}

// relayout distributes the terminal height between the fixed regions and the
// chat viewport, and rebuilds the markdown renderer for the new width.
func (m *Model) relayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	chatH := m.height - bannerHeight(m.opts.Version, m.opts.WorkDir, m.width) - inputBoxHeight - statusBarHeight
	if chatH < 1 {
		chatH = 1
	}
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(chatH)
	m.input.SetWidth(max(m.width-6, 10)) // border 2 + padding 2 + prompt slack

	wrap := max(m.width-2, 20)
	// Start from the dark style but remove the document margin and the
	// surrounding blank lines so rendered replies sit flush with their
	// role label and keep turn spacing consistent.
	sc := styles.DarkStyleConfig
	var zeroMargin uint
	sc.Document.Margin = &zeroMargin
	sc.Document.BlockPrefix = ""
	sc.Document.BlockSuffix = ""
	if r, err := glamour.NewTermRenderer(glamour.WithStyles(sc), glamour.WithWordWrap(wrap)); err == nil {
		m.renderer = r
	}
}

// render converts finished markdown to styled terminal text.
func (m Model) render(md string) string {
	if m.renderer == nil {
		return md
	}
	out, err := m.renderer.Render(md)
	if err != nil {
		return md
	}
	return strings.Trim(out, "\n")
}
