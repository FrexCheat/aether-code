package tui

import (
	"context"
	"fmt"
	"strings"
	"unicode"

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
	"github.com/FrexCheat/aether-code/internal/tui/components"
)

// wheelLines is one wheel notch, in history lines.
const wheelLines = 3

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
	Commands  *command.Registry
	Provider  config.Provider
	Version   string
	WorkDir   string
	Session   *session.Session
	// StartupNotice is shown as a status block on first render: a resume
	// notice, a fallback when --continue found nothing, a cwd warning.
	StartupNotice string
}

type Model struct {
	opts          Options
	width         int
	height        int
	composer      components.Composer
	dialog        components.PermissionDialog
	live          components.LiveRegion
	clock         components.Clock
	transcript    *transcript
	scroll        scrollState
	compacting    bool
	lastUsage     llm.Usage
	sessionWarned bool
	mcpStatus     string
}

// mcpReadyMsg carries the outcome of the background settle.
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

func (m Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case m.transcript.IsExpanded():
		return m.handleTranscriptKey(msg)
	case m.dialog.Active():
		return m.handlePermissionKey(msg)
	case m.composer.MenuActive():
		return m.handleCompletionKey(msg)
	}
	switch msg.String() {
	case "ctrl+c":
		return m.quit()
	case "shift+tab":
		return m.cyclePermissionMode()
	case "ctrl+o":
		m.transcript.ToggleExpanded()
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
	case "tab":
		return m.handleTab()
	case "enter":
		return m.handleEnter()
	}
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
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

// handleEnter is the dispatcher: a slash command runs locally, anything else
// becomes a user message.
func (m Model) handleEnter() (Model, tea.Cmd) {
	text := strings.TrimSpace(m.composer.Value())
	if text == "" {
		return m, nil
	}
	if !strings.HasPrefix(text, "/") {
		return m.sendMessage(text)
	}
	inv, ok := command.Parse(text)
	if !ok {
		// A "/" with no name is not a message either; steer it to /help.
		return m.unknownCommand(text)
	}
	cmd := m.opts.Commands.Lookup(inv.Name)
	if cmd == nil {
		return m.unknownCommand("/" + inv.Name)
	}
	if (m.live.Active() || m.compacting) && !cmd.Immediate {
		// The gate keeps the turn's loop the only writer of the history. Say
		// so: a swallowed Enter otherwise reads as a key the app dropped.
		m.transcript.Emit(block{kind: blockNotice, body: "Still working — your message stays in the box until the turn ends."})
		return m, nil
	}
	m.composer.Reset()
	h := &hostAdapter{m: &m, registry: m.opts.Commands}
	if cmd.Kind == command.Prompt {
		h.SendUserMessage(command.RenderPrompt(cmd.Prompt, inv))
	} else if err := cmd.Run(h, inv.Args); err != nil {
		h.Message(err.Error(), command.Error)
	}
	switch {
	case h.quit:
		return m.quit()
	case h.compact:
		return m.startCompact()
	case h.prompt != "":
		return m.startStream(h.prompt)
	}
	return m, nil
}

// sendMessage submits a plain message, subject to the same gate a command
// walks through.
func (m Model) sendMessage(text string) (Model, tea.Cmd) {
	if m.live.Active() || m.compacting {
		m.transcript.Emit(block{kind: blockNotice, body: "Still working — your message stays in the box until the turn ends."})
		return m, nil
	}
	return m.startStream(text)
}

// startCompact runs the forced compaction /compact asks for, spinner and all.
func (m Model) startCompact() (Model, tea.Cmd) {
	if m.opts.Compactor == nil {
		m.transcript.Emit(block{kind: blockNotice, body: "Compaction is not available for this session.", severity: noticeWarn})
		return m, nil
	}
	if m.compacting {
		return m, nil
	}
	m.compacting = true
	m.transcript.Emit(block{kind: blockNotice, body: "Compacting conversation…"})
	tick := m.clock.Start()
	return m, tea.Sequence(tick, runCompact(m.opts.Compactor))
}

func (m Model) unknownCommand(display string) (Model, tea.Cmd) {
	m.composer.Reset()
	m.transcript.Emit(block{kind: blockNotice, body: fmt.Sprintf("Unknown command %s. Type /help for a list.", display), severity: noticeError})
	return m, nil
}

// handleTab completes the command name in the box: one candidate fills it,
// several open the menu.
func (m Model) handleTab() (Model, tea.Cmd) {
	m.composer.CloseMenu()
	prefix, ok := commandPrefix(m.composer.Value())
	if !ok {
		return m, nil
	}
	matches := m.opts.Commands.Match(prefix)
	switch len(matches) {
	case 0:
		return m, nil
	case 1:
		m.composer.SetValue("/" + matches[0].Name + " ")
		return m, nil
	default:
		m.composer.OpenMenu(completionOptions(matches))
		return m, nil
	}
}

// handleCompletionKey drives the open menu: arrows move, tab or enter accept,
// esc closes, and any other key edits the box and re-filters.
func (m Model) handleCompletionKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m.quit()
	case "up":
		m.composer.MoveMenu(-1)
		return m, nil
	case "down":
		m.composer.MoveMenu(1)
		return m, nil
	case "tab", "enter":
		if option, ok := m.composer.SelectedMenu(); ok {
			m.composer.SetValue("/" + option.Name + " ")
		}
		m.composer.CloseMenu()
		return m, nil
	case "esc":
		m.composer.CloseMenu()
		return m, nil
	}
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	m.refreshMenu()
	return m, cmd
}

// refreshMenu re-filters after an edit; fewer than two candidates leave nothing
// to choose between, so the menu closes.
func (m *Model) refreshMenu() {
	prefix, ok := commandPrefix(m.composer.Value())
	if !ok {
		m.composer.CloseMenu()
		return
	}
	matches := m.opts.Commands.Match(prefix)
	if len(matches) < 2 {
		m.composer.CloseMenu()
		return
	}
	m.composer.OpenMenu(completionOptions(matches))
}

// commandPrefix reports the command-name prefix the box holds, when its whole
// value is a single slash-prefixed token — the only shape worth completing.
func commandPrefix(value string) (string, bool) {
	s := strings.TrimSpace(value)
	if !strings.HasPrefix(s, "/") {
		return "", false
	}
	rest := s[1:]
	if rest == "" {
		return "", true
	}
	if strings.IndexFunc(rest, unicode.IsSpace) >= 0 {
		return "", false
	}
	return strings.ToLower(rest), true
}

func completionOptions(cmds []*command.Command) []components.CompletionOption {
	options := make([]components.CompletionOption, len(cmds))
	for i, c := range cmds {
		options[i] = components.CompletionOption{Name: c.Name, Hint: c.ArgHint, Description: c.Description}
	}
	return options
}

func (m Model) quit() (Model, tea.Cmd) {
	m.live.Cancel()
	return m, tea.Sequence(m.shutdownMCP(), tea.Quit)
}

// clearSession rotates to a fresh session: the old archive keeps its file
// and stays resumable, the new one starts its own lock and spill directory,
// and the window starts over.
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
	m.lastUsage = llm.Usage{}
	m.sessionWarned = false
	m.transcript.Clear()
	m.scroll = scrollState{follow: true}
	m.transcript.Rebuild(m.opts.Version, m.opts.WorkDir, m.opts.Provider.Model)
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

// mcpSummary is the one-line MCP readout: how many servers and tools settled,
// the loading mode, and — so a broken server is not silent — their names. The
// per-server ✓ listing is gone; the detail lives in /status's mcp field, which
// carries this same line.
func mcpSummary(r mcp.Result) string {
	var total int
	var failed []string
	for _, s := range r.Servers {
		total += s.Tools
		if s.Err != "" {
			failed = append(failed, s.Name)
		}
	}
	// Each failed server contributes one warning; anything beyond that is a
	// configuration warning worth a count.
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
	m.composer.SetMaxHeight(min(8, max(m.height-6, 1)))
	m.live.SetWidth(m.width)
	m.transcript.SetWidth(m.width)
}
