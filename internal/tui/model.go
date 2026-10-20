package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/command"
	"github.com/FrexCheat/aether-code/internal/compaction"
	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/format"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/mcp"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/session"
	"github.com/FrexCheat/aether-code/internal/skills"
	"github.com/FrexCheat/aether-code/internal/tui/components"
)

const gateNotice = "Still working — your message stays in the box until the turn ends."

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
	Agent         *agent.Agent
	Conv          *conversation.Manager
	MCP           *mcp.Manager
	Compactor     Compactor
	Commands      *command.Registry
	Provider      config.Provider
	Version       string
	WorkDir       string
	Session       *session.Session
	Skills        *skills.State
	StartupNotice string
}

type Model struct {
	opts          Options
	width         int
	height        int
	composer      components.Composer
	dialog        components.PermissionDialog
	live          components.LiveRegion
	turn          *turnStream
	clock         components.Clock
	transcript    *transcript
	scroll        scrollState
	compacting    bool
	lastUsage     llm.Usage
	sessionWarned bool
	mcpStatus     string
}

type mcpReadyMsg struct {
	result mcp.Result
}

func Run(opts Options) error {
	_, err := tea.NewProgram(newModel(opts)).Run()
	return err
}

func newModel(opts Options) Model {
	m := Model{
		opts:       opts,
		composer:   components.NewComposer(),
		live:       components.NewLiveRegion(components.Points, components.Dots),
		scroll:     scrollState{follow: true},
		transcript: &transcript{},
	}
	var blocks []block
	if opts.Conv.Len() > 0 {
		blocks = historyBlocks(opts.Conv.Messages(), opts.Session, opts.Agent.Registry())
	}
	if opts.StartupNotice != "" {
		blocks = append(blocks, block{kind: blockStatus, body: opts.StartupNotice})
	}
	m.transcript.Load(blocks)
	if opts.Skills != nil {
		for _, name := range opts.Commands.SetDynamic(skillCommands(opts.Skills.Catalog())) {
			m.transcript.Emit(block{kind: blockNotice, body: shadowNote(name), severity: noticeWarn})
		}
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.composer.Focus(), m.connectMCP())
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
		if widthChanged {
			m.transcript.Rebuild(m.opts.Version, m.opts.WorkDir, m.opts.Provider.Model)
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
		if m.transcript.IsExpanded() {
			m.transcript.Scroll(delta)
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
		m.mcpStatus = mcpSummary(msg.result)
		if m.mcpStatus != "" {
			m.transcript.Emit(block{kind: blockStatus, body: m.mcpStatus})
		}
		return m, nil

	case compactDoneMsg:
		return m.compactDone(msg)
	}

	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	return m, cmd
}

func (m Model) View() tea.View {
	content := m.frame()
	if m.transcript.IsExpanded() {
		content = m.transcript.Screen(m.width, m.height)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *Model) stopClockIfIdle() {
	if !m.live.Active() && !m.compacting {
		m.clock.Stop()
	}
}

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
			format.Tokens(m.opts.Compactor.UsedTokens()),
			format.Tokens(m.opts.Compactor.ContextWindow()),
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

func (m Model) quit() (Model, tea.Cmd) {
	if m.turn != nil {
		m.turn.Close()
		m.turn = nil
	}
	return m, tea.Sequence(m.shutdownMCP(), tea.Quit)
}

func (m *Model) clearSession() {
	if m.opts.Session != nil {
		next, err := session.New(m.opts.Conv, m.opts.WorkDir)
		if err != nil {
			m.transcript.Emit(block{kind: blockNotice, body: "Cannot start a new session: " + err.Error(), severity: noticeError})
			return
		}
		previous := m.opts.Session
		m.opts.Session = next
		if m.opts.Compactor != nil {
			m.opts.Compactor.SetSpillDir(next.SpillDir())
		}
		previous.Close()
	}
	m.opts.Conv.Clear()
	if m.opts.Compactor != nil {
		m.opts.Compactor.Reset()
	}
	cleared := m.opts.Skills.Clear()
	m.lastUsage = llm.Usage{}
	m.sessionWarned = false
	m.transcript.Clear()
	m.scroll = scrollState{follow: true}
	m.transcript.Rebuild(m.opts.Version, m.opts.WorkDir, m.opts.Provider.Model)
	if cleared > 0 {
		m.transcript.Emit(block{kind: blockStatus, body: fmt.Sprintf("Cleared %d activated Skill(s).", cleared)})
	}
}

func (m Model) checker() *permissions.Checker {
	if m.opts.Agent == nil {
		return nil
	}
	return m.opts.Agent.Checker()
}

func (m Model) busy() bool {
	return m.turn != nil || m.compacting
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
	var failed []string
	for _, s := range r.Servers {
		total += s.Tools
		if s.Err != "" {
			failed = append(failed, s.Name)
		}
	}
	warnings := max(len(r.Warnings)-len(failed), 0)
	if len(r.Servers) == 0 && warnings == 0 {
		return ""
	}
	line := fmt.Sprintf("MCP: %d server(s), %d tool(s) · loading mode %s", len(r.Servers), total, r.Mode)
	if len(failed) > 0 {
		line += " · failed: " + strings.Join(failed, ", ")
	}
	if warnings > 0 {
		line += fmt.Sprintf(" · %d warning(s)", warnings)
	}
	return line
}

func (m *Model) reLayout() {
	if m.width <= 0 {
		return
	}
	m.composer.SetWidth(m.width)
	m.composer.SetMaxHeight(min(maxInputRows, max(m.height-6, 1)))
	m.live.SetWidth(m.width)
	m.transcript.SetWidth(m.width)
}
