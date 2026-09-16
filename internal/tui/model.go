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
	Agent        *agent.Agent
	Conv         *conversation.Manager
	Registry     *tools.Registry
	ProviderName string
	ModelName    string
	Version      string
	WorkDir      string
}

type Model struct {
	opts       Options
	conv       *conversation.Manager
	live       *liveStream // nil when idle
	running    []toolCall  // tool calls in flight, shown in the live frame
	input      textarea.Model
	spinner    spinner.Model
	width      int
	renderer   *glamour.TermRenderer
	print      func(string) tea.Cmd
	bannerDone bool
}

type toolCall struct {
	id    string
	title string
}

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
	if m.print == nil || block == "" {
		return nil
	}
	return m.print(block + "\n")
}

func diffText(r tools.ToolResult) string {
	if r.Diff {
		return r.Output
	}
	return ""
}

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

	m := Model{
		opts:    opts,
		conv:    opts.Conv,
		input:   in,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot)),
		print:   func(s string) tea.Cmd { return tea.Println(s) },
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return m.input.Focus()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.reLayout()
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
	case cmdUnknown:
		m.input.Reset()
		return m, m.println(renderNoticeBlock(hint, noticeError))
	}
	return m, m.startStream(text)
}

func (m *Model) startStream(text string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	block := renderUserBlock(m, text)
	m.conv.AddUserMessage(text)
	m.live = &liveStream{
		events: m.opts.Agent.Run(ctx, m.conv),
		ctx:    ctx,
		cancel: cancel,
		start:  timeNow(),
	}
	m.input.Reset()
	return tea.Sequence(m.println(block), tea.Batch(waitStream(m.live.events), m.spinner.Tick))
}

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
		cmd = m.println(strings.Join(out, "\n\n"))
	}
	if m.live != nil {
		return *m, tea.Sequence(cmd, waitStream(m.live.events))
	}
	return *m, cmd
}

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
	case agent.LoopError, agent.LoopComplete:
		return m.closeLive(ev)
	default:
		// ThinkingDelta, ThinkingComplete: not surfaced this phase.
	}
	return nil
}

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
		usage := ev.Usage
		switch ev.Reason {
		case agent.StoppedByModel:
			out = append(out, renderTurnBlock(timeSince(ls.start), timeNow(), usage))
		case agent.StoppedByLimit:
			out = append(out, renderNoticeBlock(
				fmt.Sprintf("Turn limit reached after %d turn(s) — the loop stopped before running out of control.", ev.TotalTurns), noticeWarn))
			out = append(out, renderTurnBlock(timeSince(ls.start), timeNow(), usage))
		case agent.StoppedByUser:
			out = append(out, renderNoticeBlock("Cancelled.", noticeInfo))
		}
	case agent.LoopError:
		out = append(out, renderNoticeBlock("Error: "+ev.Err.Error(), noticeError))
	}
	return out
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
