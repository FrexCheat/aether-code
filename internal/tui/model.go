package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/compaction"
	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/mcp"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/session"
	"github.com/FrexCheat/aether-code/internal/tui/components"
)

// wheelLines is one wheel notch, in history lines.
const wheelLines = 3

const availableCommands = "/quit, /exit, /clear, /compact"

// Compactor is the window's view of the compaction layer.
type Compactor interface {
	UsedTokens() int
	ContextWindow() int
	Level() compaction.Level
	ForceCompact(ctx context.Context, notify func(compaction.Progress)) (compaction.Result, error)
	Reset()
	SetSpillDir(dir string)
}

var _ Compactor = (*compaction.Manager)(nil)

type Options struct {
	Agent     *agent.Agent
	Conv      *conversation.Manager
	MCP       *mcp.Manager
	Compactor Compactor
	Provider  config.Provider
	Version   string
	WorkDir   string
	Session   *session.Session
	// StartupNotice is shown as a status block on first render: a resume
	// notice, a fallback when --continue found nothing, a cwd warning.
	StartupNotice string
}

type Model struct {
	opts              Options
	width             int
	height            int
	ready             bool
	input             components.InputBox
	dialog            components.PermissionDialog
	live              components.LiveRegion
	clock             components.Clock
	compacting        bool
	compactionSpinner components.Spinner
	renderer          blockRenderer
	blocks            []block
	history           []string
	scroll            scrollState
	transcript        *transcriptView
	lastUsage         llm.Usage
	session           *session.Session
	sessionWarned     bool
}

type command int

const (
	cmdNone command = iota
	cmdQuit
	cmdClear
	cmdCompact
	cmdUnknown
)

// mcpReadyMsg carries the outcome of the background settle.
type mcpReadyMsg struct {
	result mcp.Result
}

// parseCommand classifies user input; for cmdUnknown the string is the hint.
func parseCommand(input string) (command, string) {
	s := strings.TrimSpace(input)
	if !strings.HasPrefix(s, "/") {
		return cmdNone, ""
	}
	switch s {
	case "/quit", "/exit":
		return cmdQuit, ""
	case "/clear":
		return cmdClear, ""
	case "/compact":
		return cmdCompact, ""
	}
	return cmdUnknown, fmt.Sprintf("Unknown command %q. Available: %s", s, availableCommands)
}

func Run(opts Options) error {
	_, err := tea.NewProgram(newModel(opts)).Run()
	return err
}

func newModel(opts Options) Model {
	m := Model{
		opts:              opts,
		input:             components.NewInputBox(),
		live:              components.NewLiveRegion(components.Points, components.Dots),
		compactionSpinner: components.Dots,
		scroll:            scrollState{follow: true},
		transcript:        &transcriptView{},
		session:           opts.Session,
	}
	if opts.Conv.Len() > 0 {
		m.blocks = historyBlocks(opts.Conv.Messages(), opts.Session, opts.Agent.Registry())
	}
	if opts.StartupNotice != "" {
		m.blocks = append(m.blocks, block{kind: blockStatus, body: opts.StartupNotice})
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.input.Focus(), m.connectMCP())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m.update(msg)
}

func (m Model) update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		widthChanged := m.width != msg.Width
		m.width, m.height = msg.Width, msg.Height
		m.reLayout()
		if widthChanged || !m.ready {
			m.ready = true
			m.rebuildHistory()
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseWheelMsg:
		delta := 0
		switch msg.Button {
		case tea.MouseWheelUp:
			delta = -wheelLines
		case tea.MouseWheelDown:
			delta = wheelLines
		}
		if m.transcript.open {
			m.transcript.scroll(delta)
		} else {
			m.scrollBy(delta)
		}
		return m, nil

	case components.TickMsg:
		next := m.clock.Update(msg)
		return m, next

	case streamBatchMsg:
		return m.handleStreamBatch(msg)

	case mcpReadyMsg:
		if text := mcpSummary(msg.result); text != "" {
			m.emit(block{kind: blockStatus, body: text})
		}
		return m, nil

	case compactDoneMsg:
		return m.compactDone(msg)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) View() tea.View {
	content := m.frame()
	if m.transcript.open {
		content = m.transcriptScreen()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// stopClockIfIdle ends the animation clock once nothing is animating.
func (m *Model) stopClockIfIdle() {
	if !m.live.Active() && !m.compacting {
		m.clock.Stop()
	}
}

// cacheHitRate is the cached share of the prompt the last request carried.
func cacheHitRate(u llm.Usage) int {
	if denom := u.PromptTokens(); denom > 0 {
		return u.CacheReadTokens * 100 / denom
	}
	return 0
}

func (m Model) footer() components.FooterData {
	f := components.FooterData{Scrolled: !m.scroll.follow}
	if checker := m.checker(); checker != nil {
		f.Mode = string(checker.Mode())
	}
	if m.opts.Compactor != nil {
		f.Usage = fmt.Sprintf("%s / %s · %d%% cache hit",
			formatTokenCount(m.opts.Compactor.UsedTokens()),
			formatTokenCount(m.opts.Compactor.ContextWindow()),
			cacheHitRate(m.lastUsage))
		switch m.opts.Compactor.Level() {
		case compaction.LevelWarn:
			f.Usage += " · context low"
		case compaction.LevelForce:
			f.Usage += " · context critical — /compact"
		}
	}
	return f
}

func (m Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case m.transcript.open:
		return m.handleTranscriptKey(msg)
	case m.dialog.Active():
		return m.handlePermissionKey(msg)
	}
	switch msg.String() {
	case "ctrl+c":
		return m.quit()
	case "shift+tab":
		return m.cyclePermissionMode()
	case "ctrl+o":
		m.toggleTranscript()
		return m, nil
	case "esc":
		m.live.Cancel()
		return m, nil
	case "pgup":
		m.scrollBy(-m.pageLines())
		return m, nil
	case "pgdown":
		m.scrollBy(m.pageLines())
		return m, nil
	case "enter":
		return m.handleEnter()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) handlePermissionKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.answerPermission(permissions.Reject)
		return m.quit()
	case "up":
		m.dialog.Move(-1)
	case "down":
		m.dialog.Move(1)
	case "enter":
		m.answerPermission(m.dialog.Selected())
	case "esc":
		m.answerPermission(permissions.Reject)
	}
	return m, nil
}

func (m *Model) answerPermission(response permissions.Response) {
	req := m.dialog.Close()
	if req == nil {
		return
	}
	m.live.Resume()
	select {
	case req.Reply <- response:
	default:
	}
}

func (m Model) handleEnter() (Model, tea.Cmd) {
	if m.live.Active() || m.compacting {
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
		m.input.Reset()
		// /clear is a new session, not an edit of the current one: the old
		// archive keeps its file and stays resumable, the new one starts its
		// own lock and spill directory, and the window starts over.
		if m.session != nil {
			next, err := session.New(m.opts.Conv, m.opts.WorkDir)
			if err != nil {
				m.emit(block{kind: blockNotice, body: "Cannot start a new session: " + err.Error(), severity: noticeError})
				return m, nil
			}
			previous := m.session
			m.session = next
			if m.opts.Compactor != nil {
				m.opts.Compactor.SetSpillDir(next.SpillDir())
			}
			previous.Close()
		}
		m.opts.Conv.Clear()
		if m.opts.Compactor != nil {
			m.opts.Compactor.Reset()
		}
		m.lastUsage = llm.Usage{}
		m.sessionWarned = false
		m.blocks = nil
		m.scroll = scrollState{follow: true}
		m.rebuildHistory()
		return m, nil
	case cmdCompact:
		m.input.Reset()
		if m.opts.Compactor == nil {
			m.emit(block{kind: blockNotice, body: "Compaction is not available for this session.", severity: noticeWarn})
			return m, nil
		}
		if m.compacting {
			return m, nil
		}
		m.compacting = true
		m.emit(block{kind: blockNotice, body: "Compacting conversation…"})
		tick := m.clock.Start()
		return m, tea.Sequence(tick, runCompact(m.opts.Compactor))
	case cmdUnknown:
		m.input.Reset()
		m.emit(block{kind: blockNotice, body: hint, severity: noticeError})
		return m, nil
	}
	return m.startStream(text)
}

func (m Model) quit() (Model, tea.Cmd) {
	m.live.Cancel()
	return m, tea.Sequence(m.shutdownMCP(), tea.Quit)
}

func (m Model) checker() *permissions.Checker {
	if m.opts.Agent == nil {
		return nil
	}
	return m.opts.Agent.Checker()
}

func (m Model) cyclePermissionMode() (Model, tea.Cmd) {
	checker := m.checker()
	if checker == nil {
		return m, nil
	}
	checker.SetMode(permissions.NextMode(checker.Mode()))
	return m, nil
}

func (m Model) connectMCP() tea.Cmd {
	if m.opts.MCP == nil {
		return nil
	}
	mgr, registry := m.opts.MCP, m.opts.Agent.Registry()
	provider := m.opts.Provider
	return func() tea.Msg {
		return mcpReadyMsg{mgr.Settle(context.Background(), registry, provider.Protocol, provider.BaseURL)}
	}
}

func (m Model) shutdownMCP() tea.Cmd {
	mgr := m.opts.MCP
	return func() tea.Msg {
		mgr.Shutdown()
		return nil
	}
}

func mcpSummary(r mcp.Result) string {
	var total int
	for _, s := range r.Servers {
		total += s.Tools
	}
	if len(r.Servers) == 0 && len(r.Warnings) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "MCP: %d server(s), %d tool(s), loading mode %s\n", len(r.Servers), total, r.Mode)
	for _, s := range r.Servers {
		if s.Err != "" {
			fmt.Fprintf(&b, "  ✗ %s: %s\n", s.Name, s.Err)
			continue
		}
		fmt.Fprintf(&b, "  ✓ %s: %d tool(s)\n", s.Name, s.Tools)
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "  warning: %s\n", w)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (m *Model) reLayout() {
	if m.width <= 0 {
		return
	}
	m.input.SetWidth(m.width)
	m.input.SetMaxHeight(min(8, max(m.height-6, 1)))
	m.live.SetWidth(m.width)
	m.renderer.SetWidth(m.width)
}
