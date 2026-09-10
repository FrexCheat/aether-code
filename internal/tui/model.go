// Package tui implements the interactive terminal interface: banner, chat
// area, input box, status bar, streaming consumption and slash commands.
package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"

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

// entry is one record in the chat area.
type entry struct {
	role     conversation.Role
	text     string
	err      error
	started  time.Time
	elapsed  time.Duration
	doneAt   time.Time
	done     bool
	rendered string
}

// Model is the Bubble Tea model for the chat UI.
type Model struct {
	opts    Options
	conv    *conversation.Manager
	entries []entry

	viewport viewport.Model
	input    textinput.Model
	width    int
	height   int

	streaming   bool
	events      <-chan llm.StreamEvent
	cancel      context.CancelFunc
	pendingUser string
	renderer    *glamour.TermRenderer
}

type streamEventMsg llm.StreamEvent
type tickMsg time.Time

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

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.refresh()
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m.quit()
		case "enter":
			return m.handleEnter()
		}

	case streamEventMsg:
		return m.handleStreamEvent(llm.StreamEvent(msg))

	case tickMsg:
		if m.streaming {
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

func (m Model) quit() (tea.Model, tea.Cmd) {
	if m.cancel != nil {
		m.cancel()
	}
	return m, tea.Quit
}

func (m Model) handleEnter() (tea.Model, tea.Cmd) {
	if m.streaming {
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
		m.entries = append(m.entries, entry{role: conversation.RoleAssistant, err: errors.New(hint), done: true})
		m.input.Reset()
		m.refresh()
		return m, nil
	}
	c := m.startStream(text)
	m.refresh()
	return m, c
}

// startStream begins a new turn for text and returns the commands that drive it.
func (m *Model) startStream(text string) tea.Cmd {
	m.entries = append(m.entries,
		entry{role: conversation.RoleUser, text: text, done: true},
		entry{role: conversation.RoleAssistant, started: time.Now()},
	)
	m.pendingUser = text
	m.input.Reset()

	msgs := append(m.conv.GetMessages(), conversation.Message{Role: conversation.RoleUser, Content: text})
	req := llm.Request{
		System:   prompt.System,
		Messages: msgs,
		Thinking: m.opts.Thinking,
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.events = m.opts.Provider.Stream(ctx, req)
	m.streaming = true
	return tea.Batch(waitForEvent(m.events), tick())
}

func (m Model) handleStreamEvent(ev llm.StreamEvent) (tea.Model, tea.Cmd) {
	if len(m.entries) == 0 {
		return m, nil
	}
	last := &m.entries[len(m.entries)-1]
	switch e := ev.(type) {
	case llm.TextDelta:
		last.text += e.Text
	case llm.ThinkingDelta, llm.ThinkingComplete, llm.ToolCallStart, llm.ToolCallDelta, llm.ToolCallComplete:
		// Rendered in a later phase; keep the stream draining.
	case llm.StreamEnd:
		last.elapsed = time.Since(last.started)
		last.doneAt = time.Now()
		last.done = true
		last.rendered = m.render(last.text)
		m.conv.AddUserMessage(m.pendingUser)
		m.conv.AddAssistantMessage(last.text)
		m.finishStream()
	case llm.StreamError:
		last.err = e.Err
		last.done = true
		m.finishStream()
	}
	m.refresh()
	if m.streaming {
		return m, waitForEvent(m.events)
	}
	return m, nil
}

func (m *Model) finishStream() {
	m.streaming = false
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.events = nil
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

func waitForEvent(ch <-chan llm.StreamEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return streamEventMsg(ev)
	}
}

// tickInterval is the timer refresh period; tests shorten it.
var tickInterval = time.Second

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}
