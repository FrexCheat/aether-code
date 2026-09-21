package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/compaction"
	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/mcp"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/tools"
)

type Options struct {
	Agent        *agent.Agent
	Conv         *conversation.Manager
	Registry     *tools.Registry
	MCP          *mcp.Manager
	Compactor    *compaction.Manager
	Provider     config.Provider
	ProviderName string
	ModelName    string
	Version      string
	WorkDir      string
}

type Model struct {
	opts             Options
	live             *liveStream          // nil when idle
	permission       *permissions.Request // non-nil while the confirmation prompt is open
	permissionCursor int
	compacting       bool // /compact in flight; no new turns while history is being rewritten
	input            textarea.Model
	spinner          spinner.Model
	width            int
	height           int
	renderer         *glamour.TermRenderer
	isBannerPrinted  bool
	transcript       transcriptView
}

const titleClampChars = 160

// toolTitle is the one place tool titles get trimmed for display: one line,
// titleClampChars runes. Titles never reach the history or the model, so the
// clamp is a pure display concern.
func toolTitle(reg *tools.Registry, name string, args map[string]any) string {
	title := name
	if reg != nil {
		if t := reg.Get(name); t != nil {
			if tt, ok := t.(tools.TitledTool); ok {
				if s := tt.Title(args); s != "" {
					title = s
				}
			}
		}
	}
	title = strings.Join(strings.Fields(title), " ")
	if runes := []rune(title); len(runes) > titleClampChars {
		return string(runes[:titleClampChars]) + "…"
	}
	return title
}

func lineContinues(in textarea.Model) bool {
	value := in.Value()
	if !strings.HasSuffix(strings.TrimRight(value, " \t"), `\`) {
		return false
	}
	lines := strings.Split(value, "\n")
	return in.Line() == len(lines)-1 && in.Column() == len([]rune(lines[len(lines)-1]))
}

func continueValue(value string) string {
	return strings.TrimSuffix(strings.TrimRight(value, " \t"), `\`) + "\n"
}

func (m *Model) println(block string) tea.Cmd {
	if block == "" {
		return nil
	}
	return tea.Println(block + "\n")
}

func (m *Model) flushText() []string {
	if m.live == nil {
		return nil
	}
	if text := m.live.text.String(); text != "" {
		m.live.text.Reset()
		m.record(transcriptItem{kind: "assistant", body: text})
		return []string{m.components().assistant(text)}
	}
	return nil
}

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
		input:   in,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.input.Focus(), m.connectMCP())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.reLayout()
		if !m.isBannerPrinted {
			m.isBannerPrinted = true
			return m, m.println(m.components().banner(m.opts.Version, m.opts.WorkDir))
		}
		return m, nil

	case tea.KeyPressMsg:
		if m.transcript.open {
			return m.handleTranscriptKey(msg)
		}
		if m.permission != nil {
			return m.handlePermissionKey(msg)
		}
		switch msg.String() {
		case "ctrl+c":
			return m.quit()
		case "shift+tab":
			return m.cyclePermissionMode()
		case "ctrl+o":
			return m.toggleTranscript()
		case "esc":
			if m.live != nil {
				m.live.cancel()
			}
			return m, nil
		case "enter":
			return m.handleEnter()
		}

	case streamBatchMsg:
		return m.handleStreamBatch(msg)

	case mcpReadyMsg:
		if text := mcpSummary(msg.result); text != "" {
			m.record(transcriptItem{kind: "notice", body: text})
			return m, m.println(text)
		}
		return m, nil

	case compactDoneMsg:
		return m.compactDone(msg)

	case spinner.TickMsg:
		if m.live == nil && !m.compacting {
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

func (m Model) View() tea.View {
	if m.transcript.open {
		v := tea.NewView(m.transcriptScreen())
		v.AltScreen = true
		v.MouseMode = tea.MouseModeNone
		return v
	}
	c := m.components()
	var parts []string
	if m.permission != nil {
		parts = append(parts, c.permissionPrompt(m.permission, m.permissionCursor))
	} else if m.compacting {
		parts = append(parts, c.compacting())
	} else if m.live != nil {
		for _, call := range m.live.tools {
			parts = append(parts, c.toolRunning(call.title))
		}
		parts = append(parts, c.live(int(m.live.elapsed().Seconds())))
	}
	mode := ""
	if checker := m.checker(); checker != nil {
		mode = string(checker.Mode())
	}
	parts = append(parts, c.input(m.input.View()), c.status(m.opts.ProviderName, mode, m.opts.ModelName))

	v := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, parts...))
	v.AltScreen = false
	v.MouseMode = tea.MouseModeNone
	return v
}

func (m Model) quit() (Model, tea.Cmd) {
	if m.live != nil {
		m.live.cancel()
	}
	return m, tea.Sequence(m.shutdownMCP(), tea.Quit)
}

func (m *Model) checker() *permissions.Checker {
	if m.opts.Agent == nil {
		return nil
	}
	return m.opts.Agent.Checker()
}

func (m Model) cyclePermissionMode() (tea.Model, tea.Cmd) {
	checker := m.checker()
	if checker == nil {
		return m, nil
	}
	checker.SetMode(permissions.NextMode(checker.Mode()))
	return m, nil
}

func (m Model) handlePermissionKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	choices := m.permissionChoices()
	switch msg.String() {
	case "ctrl+c":
		m.answerPermission(permissions.Reject)
		return m.quit()
	case "up":
		m.permissionCursor = (m.permissionCursor + len(choices) - 1) % len(choices)
	case "down":
		m.permissionCursor = (m.permissionCursor + 1) % len(choices)
	case "enter":
		m.answerPermission(choices[m.permissionCursor].response)
		return m, nil
	case "esc":
		m.answerPermission(permissions.Reject)
		return m, nil
	}
	return m, nil
}

// permissionChoices is the option list for the question on screen, which is
// shorter when the call offers no rule to record.
func (m Model) permissionChoices() []permissionOption {
	if m.permission == nil {
		return permissionOptions
	}
	return permissionOptionsFor(m.permission)
}

func (m *Model) answerPermission(response permissions.Response) {
	request := m.permission
	if request == nil {
		return
	}
	m.permission = nil
	m.permissionCursor = 0
	if m.live != nil && !m.live.permissionSince.IsZero() {
		m.live.permissionWait += time.Since(m.live.permissionSince)
		m.live.permissionSince = time.Time{}
	}
	select {
	case request.Reply <- response:
	default:
	}
}

func (m Model) handleEnter() (Model, tea.Cmd) {
	if m.live != nil || m.compacting {
		return m, nil // input is ignored while a turn or a compaction is in flight
	}
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
		m.opts.Conv.Clear()
		if m.opts.Compactor != nil {
			m.opts.Compactor.Reset()
		}
		m.input.Reset()
		m.record(transcriptItem{kind: "notice", body: "— context cleared —"})
		return m, m.println(m.components().cleared())
	case cmdCompact:
		m.input.Reset()
		if m.opts.Compactor == nil {
			m.record(transcriptItem{kind: "notice", body: "Compaction is not available for this session."})
			return m, m.println(m.components().notice("Compaction is not available for this session.", noticeWarn))
		}
		if m.compacting {
			return m, nil
		}
		m.compacting = true
		m.record(transcriptItem{kind: "notice", body: "Compacting conversation…"})
		notice := m.println(m.components().notice("Compacting conversation…", noticeInfo))
		return m, tea.Sequence(notice, tea.Batch(runCompact(m.opts.Compactor), m.spinner.Tick))
	case cmdUnknown:
		m.input.Reset()
		m.record(transcriptItem{kind: "notice", body: hint, isErr: true})
		return m, m.println(m.components().notice(hint, noticeError))
	}
	return m, m.startStream(text)
}

type compactDoneMsg struct {
	result compaction.Result
	err    error
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
		m.record(transcriptItem{kind: "notice", body: "Compaction failed: " + msg.err.Error(), isErr: true})
		return m, m.println(m.components().notice("Compaction failed: "+msg.err.Error(), noticeError))
	case !msg.result.Compacted:
		m.record(transcriptItem{kind: "notice", body: "Nothing to compact."})
		return m, m.println(m.components().notice("Nothing to compact.", noticeInfo))
	default:
		text := fmt.Sprintf("Context compacted: %s → %s tokens",
			formatTokenCount(msg.result.FromTokens), formatTokenCount(msg.result.ToTokens))
		m.record(transcriptItem{kind: "notice", body: text})
		return m, m.println(m.components().notice(text, noticeInfo))
	}
}

func (m *Model) startStream(text string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	block := m.components().user(text)
	m.opts.Conv.AddUserMessage(text)
	m.record(transcriptItem{kind: "user", body: text})
	m.live = &liveStream{
		events: m.opts.Agent.Run(ctx, m.opts.Conv),
		cancel: cancel,
		start:  time.Now(),
	}
	m.input.Reset()
	return tea.Sequence(m.println(block), tea.Batch(waitStream(m.live.events), m.spinner.Tick))
}

func (m *Model) handleStreamBatch(batch streamBatchMsg) (Model, tea.Cmd) {
	var out []string
	for _, ev := range batch {
		out = append(out, m.foldEvent(ev)...)
		if m.live == nil { // a terminal event consumed; drop what follows it
			break
		}
	}
	var cmd tea.Cmd
	if len(out) > 0 {
		cmd = m.println(strings.Join(out, "\n\n"))
	}
	if m.live != nil {
		return *m, tea.Sequence(cmd, waitStream(m.live.events))
	}
	return *m, cmd
}

func (m *Model) foldEvent(ev agent.Event) []string {
	switch e := ev.(type) {
	case agent.TextDelta:
		if m.live != nil {
			m.live.text.WriteString(e.Text)
		}
	case agent.TextComplete:
		return m.flushText()
	case agent.ToolCallBegin:
		if m.live != nil {
			m.live.beginTool(e.ToolID, toolTitle(m.opts.Registry, e.ToolName, e.Args))
		}
	case agent.ToolCallEnd:
		if m.live != nil {
			if call, ok := m.live.endTool(e.ToolID); ok {
				m.record(transcriptItem{
					kind:   "tool",
					title:  call.title,
					body:   e.Result.Output,
					isErr:  e.Result.IsError,
					isDiff: e.Result.IsDiff,
				})
				return []string{m.components().toolDone(call.title, e.Result.IsError, e.Elapsed, e.Result)}
			}
		}
	case agent.CompactionStart:
		return m.noticeOut("Compacting conversation…", noticeInfo)
	case agent.CompactionDone:
		return m.noticeOut(fmt.Sprintf("Context compacted: %s → %s tokens",
			formatTokenCount(e.FromTokens), formatTokenCount(e.ToTokens)), noticeInfo)
	case agent.CompactionFailed:
		return m.noticeOut("Compaction failed: "+e.Err.Error(), noticeWarn)
	case agent.PermissionRequestEvent:
		m.permission = &e.Request
		m.permissionCursor = 0
		if m.live != nil {
			m.live.permissionSince = time.Now()
		}
	case agent.LoopError, agent.LoopComplete:
		return m.closeLive(ev)
	default:
		// ThinkingDelta, ThinkingComplete: not surfaced this phase.
	}
	return nil
}

func (m *Model) closeLive(ev agent.Event) []string {
	ls := m.live
	if ls == nil {
		return nil
	}
	defer func() {
		m.answerPermission(permissions.Reject)
		ls.cancel()
		m.live = nil
	}()
	out := m.flushText()
	c := m.components()
	switch ev := ev.(type) {
	case agent.LoopComplete:
		usage := ev.Usage
		switch ev.Reason {
		case agent.StoppedByModel:
			m.record(transcriptItem{kind: "notice", body: turnLine(ls.elapsed(), time.Now(), usage)})
			out = append(out, c.turn(ls.elapsed(), time.Now(), usage))
		case agent.StoppedByLimit:
			limit := fmt.Sprintf(
				"Round limit reached after %d tool rounds — the loop stopped before running out of control.",
				ev.ToolRounds)
			m.record(transcriptItem{kind: "notice", body: limit})
			m.record(transcriptItem{kind: "notice", body: turnLine(ls.elapsed(), time.Now(), usage)})
			out = append(out, c.notice(limit, noticeWarn))
			out = append(out, c.turn(ls.elapsed(), time.Now(), usage))
		case agent.StoppedByUser:
			m.record(transcriptItem{kind: "notice", body: "Cancelled."})
			out = append(out, c.notice("Cancelled.", noticeInfo))
		}
	case agent.LoopError:
		m.record(transcriptItem{kind: "notice", body: "Error: " + ev.Err.Error(), isErr: true})
		out = append(out, c.notice("Error: "+ev.Err.Error(), noticeError))
	}
	return out
}

// noticeOut folds an automatic-path notice into the batch being printed and
// records it for the transcript.
func (m *Model) noticeOut(text string, sev noticeSeverity) []string {
	m.record(transcriptItem{kind: "notice", body: text, isErr: sev == noticeError})
	return []string{m.components().notice(text, sev)}
}

func (m *Model) reLayout() {
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
