package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/prompt"
	"github.com/FrexCheat/aether-code/internal/tools"
)

// fakeProvider replays one event list per request (index-matched via scripts,
// falling back to the single events list) and records the last request.
type fakeProvider struct {
	events  []llm.StreamEvent   // replayed for every request
	scripts [][]llm.StreamEvent // per-request scripts; wins over events when set
	block   bool                // if true, never send or close (simulates an in-flight stream)
	calls   atomic.Int32
	lastReq llm.Request
}

func (f *fakeProvider) Stream(ctx context.Context, req llm.Request) <-chan llm.StreamEvent {
	f.calls.Add(1)
	f.lastReq = req
	if f.block {
		return make(chan llm.StreamEvent) // never sent, never closed: in-flight forever
	}
	script := f.events
	if f.scripts != nil {
		idx := int(f.calls.Load()) - 1
		if idx >= len(f.scripts) {
			idx = len(f.scripts) - 1
		}
		script = f.scripts[idx]
	}
	ch := make(chan llm.StreamEvent, len(script)+1)
	for _, ev := range script {
		ch <- ev
	}
	close(ch)
	return ch
}

// printRecorder captures the blocks a real run would print above the live
// frame, in commit order.
type printRecorder struct{ chunks []string }

func (r *printRecorder) record(block string) tea.Cmd {
	r.chunks = append(r.chunks, block)
	return nil
}

// joined concatenates all captured chunks the way they would appear in the
// terminal buffer: each chunk already carries its separating blank line, the
// join adds the newline between separately printed chunks.
func (r *printRecorder) joined() string { return strings.Join(r.chunks, "\n") }

// lastChunk returns the most recently captured block.
func (r *printRecorder) lastChunk() string {
	if len(r.chunks) == 0 {
		return ""
	}
	return r.chunks[len(r.chunks)-1]
}

func newTestModel(p llm.Client) (Model, *printRecorder) {
	conv := conversation.NewManager()
	reg := tools.NewRegistry()
	// The real read_file stands in for titled tools: its Title derives from
	// the call args alone, no filesystem access.
	reg.Register(&tools.ReadFileTool{})
	ag := agent.New(p, reg, conv, prompt.System, false, config.DefaultMaxIterations)
	m := newModel(Options{
		Agent: ag, Conv: conv, Registry: reg, ProviderName: "fake", ModelName: "fake-1",
		Version: "test", WorkDir: "/tmp",
	})
	rec := &printRecorder{}
	m.print = rec.record
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return mm.(Model), rec
}

// drive feeds msg to Update, then executes returned commands until none remain.
// spinner.TickMsg results are not re-fed, so the loop terminates once the
// stream ends.
func drive(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	queue := []tea.Msg{msg}
	deadline := time.Now().Add(5 * time.Second)
	for len(queue) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("drive: timed out")
		}
		cur := queue[0]
		queue = queue[1:]
		if cur == nil {
			continue
		}
		mm, cmd := m.Update(cur)
		m = mm.(Model)
		queue = append(queue, run(cmd)...)
	}
	return m
}

func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch v := msg.(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range v {
			out = append(out, run(c)...)
		}
		return out
	case spinner.TickMsg:
		return nil
	case streamBatchMsg:
		return []tea.Msg{v}
	}
	return []tea.Msg{msg}
}

func enter() tea.Msg { return tea.KeyPressMsg{Code: tea.KeyEnter} }

func send(t *testing.T, m Model, text string) Model {
	t.Helper()
	m.input.SetValue(text)
	return drive(t, m, enter())
}

func TestSendCommitsWholeOnDone(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{llm.TextDelta{Text: "Hi"}, llm.StreamEnd{}}}
	m, rec := newTestModel(fake)
	m = send(t, m, "hello")
	if m.conv.Len() != 2 {
		t.Errorf("conv.Len = %d, want 2", m.conv.Len())
	}
	if m.live != nil {
		t.Error("stream should be idle after Done")
	}
	if len(fake.lastReq.Messages) != 1 || fake.lastReq.System != prompt.System {
		t.Errorf("request = %+v", fake.lastReq)
	}
	// Printed history: opening block, user bubble, then reply and footer
	// (separately printed or merged into one chunk, order holds either way).
	if len(rec.chunks) != 3 {
		t.Errorf("printed chunks = %d, want 3:\n%s", len(rec.chunks), rec.joined())
	}
	chat := ansi.Strip(rec.joined())
	if !strings.Contains(chat, "hello") || !strings.Contains(chat, "Hi") || !strings.Contains(chat, "· done") {
		t.Errorf("history should show question, reply and turn footer:\n%s", chat)
	}
}

// TestMessageRenderStyles locks the headerless message presentation: user
// text on a full-width gray bar, assistant text behind a plain white dot, and
// one blank line between messages.
func TestMessageRenderStyles(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{llm.TextDelta{Text: "Hi"}, llm.StreamEnd{}}}
	m, rec := newTestModel(fake)
	m = send(t, m, "hello")
	if raw := rec.chunks[1]; raw != styleUserBubble.Width(m.width).Render("hello")+"\n" {
		t.Errorf("user message should print as the full-width gray bar plus one blank line:\n%q", raw)
	}
	chat := ansi.Strip(rec.joined())
	if !strings.Contains(chat, "● Hi") {
		t.Errorf("assistant reply should lead with a plain dot:\n%s", chat)
	}
	if strings.Contains(chat, "❯") || strings.Contains(chat, "● AetherCode") {
		t.Errorf("role headers should be gone:\n%s", chat)
	}
	if !strings.Contains(chat, "\n\n● Hi") {
		t.Errorf("messages should be separated by one blank line:\n%s", chat)
	}
}

func TestStreamingTextHiddenUntilCommit(t *testing.T) {
	fake := &fakeProvider{block: true}
	m, rec := newTestModel(fake)
	m.input.SetValue("q")
	mm, _ := m.Update(enter()) // start the turn; discard the blocking waitStream cmd
	m = mm.(Model)
	if m.live == nil {
		t.Fatal("expected live stream after enter")
	}
	// Fold a text-only batch (no terminal event): text accumulates but stays
	// off both the live frame and the printed history.
	mm, _ = m.Update(streamBatchMsg([]agent.AgentEvent{agent.TextDelta{Text: "secret"}}))
	m = mm.(Model)
	frame := ansi.Strip(m.View().Content)
	if strings.Contains(frame, "secret") {
		t.Errorf("in-flight reply text must stay hidden from the live frame:\n%s", frame)
	}
	if !strings.Contains(frame, "Imagining") {
		t.Errorf("live frame should show loading indicator:\n%s", frame)
	}
	if strings.Contains(ansi.Strip(rec.joined()), "secret") {
		t.Error("in-flight reply text must not print")
	}
	m.live.cancel()
}

func TestSpinnerAdvancesOnTick(t *testing.T) {
	fake := &fakeProvider{block: true}
	m, _ := newTestModel(fake)
	m.input.SetValue("q")
	mm, _ := m.Update(enter())
	m = mm.(Model)
	f1 := m.spinner.View()
	mm, _ = m.Update(spinner.TickMsg{})
	m = mm.(Model)
	f2 := m.spinner.View()
	if f1 == f2 {
		t.Error("spinner frame should advance on tick")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "Imagining") {
		t.Error("live frame should include the loading indicator")
	}
	m.live.cancel()
}

func TestSecondTurnCarriesHistory(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{llm.TextDelta{Text: "A"}, llm.StreamEnd{}}}
	m, _ := newTestModel(fake)
	m = send(t, m, "q1")
	m = send(t, m, "q2")
	msgs := fake.lastReq.Messages
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3", len(msgs))
	}
	wantRoles := []conversation.Role{conversation.RoleUser, conversation.RoleAssistant, conversation.RoleUser}
	for i, r := range wantRoles {
		if msgs[i].Role != r {
			t.Errorf("messages[%d].Role = %s, want %s", i, msgs[i].Role, r)
		}
	}
	if msgs[1].Content != "A" || msgs[2].Content != "q2" {
		t.Errorf("messages = %+v", msgs)
	}
}

func TestClearResetsSession(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{llm.StreamEnd{}}}
	m, rec := newTestModel(fake)
	m = send(t, m, "q1")
	m = send(t, m, "/clear")
	if m.conv.Len() != 0 {
		t.Errorf("after /clear: conv=%d", m.conv.Len())
	}
	if fake.calls.Load() != 1 {
		t.Errorf("/clear must not call Stream, calls = %d", fake.calls.Load())
	}
	if !strings.Contains(ansi.Strip(rec.lastChunk()), "context cleared") {
		t.Errorf("/clear should print a marker, got %q", rec.lastChunk())
	}
}

func TestUnknownCommandNoStream(t *testing.T) {
	fake := &fakeProvider{}
	m, rec := newTestModel(fake)
	m = send(t, m, "/foo")
	if fake.calls.Load() != 0 {
		t.Errorf("Stream called %d times", fake.calls.Load())
	}
	if last := ansi.Strip(rec.lastChunk()); !strings.Contains(last, "Unknown command") {
		t.Errorf("notice chunk = %q", last)
	}
}

func TestEnterIgnoredWhileStreaming(t *testing.T) {
	fake := &fakeProvider{block: true}
	m, _ := newTestModel(fake)
	m.input.SetValue("first")
	mm, _ := m.Update(enter()) // do not run the blocking waitStream cmd
	m = mm.(Model)
	if m.live == nil {
		t.Fatal("expected live stream after first enter")
	}
	// The agent calls Stream from its own goroutine; wait for it to land.
	deadline := time.Now().Add(2 * time.Second)
	for fake.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	m.input.SetValue("second")
	mm, _ = m.Update(enter())
	m = mm.(Model)
	if fake.calls.Load() != 1 {
		t.Errorf("Stream calls = %d, want 1", fake.calls.Load())
	}
	if m.live != nil {
		m.live.cancel()
	}
}

func TestTypingFillsFocusedInput(t *testing.T) {
	m, _ := newTestModel(&fakeProvider{})
	if !m.input.Focused() {
		t.Fatal("input must be focused from the start")
	}
	for _, ch := range "hi" {
		mm, _ := m.Update(tea.KeyPressMsg{Text: string(ch)})
		m = mm.(Model)
	}
	if got := m.input.Value(); got != "hi" {
		t.Errorf("typed value = %q, want %q", got, "hi")
	}
	if stripped := ansi.Strip(m.View().Content); strings.Count(stripped, "❯") != 0 {
		t.Errorf("input should render no prompt, got:\n%s", stripped)
	}
}

func TestViewLiveFrameAndModes(t *testing.T) {
	m, rec := newTestModel(&fakeProvider{})
	v := ansi.Strip(m.View().Content)
	for _, want := range []string{"fake", "fake-1"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q", want)
		}
	}
	// The banner is printed once into the history, not part of the live frame.
	if strings.Contains(v, "AetherCode") {
		t.Errorf("live frame should not contain the banner:\n%s", v)
	}
	if m.View().AltScreen {
		t.Error("AltScreen should be disabled for native scrollback")
	}
	if m.View().MouseMode != tea.MouseModeNone {
		t.Error("MouseMode should be None so wheel scrolling and selection stay native")
	}
	// The first size report printed the opening block.
	if len(rec.chunks) != 1 {
		t.Fatalf("chunks after size = %d, want 1", len(rec.chunks))
	}
	if open := rec.chunks[0]; !strings.Contains(open, "AetherCode") || !strings.Contains(open, "Press Enter to send") {
		t.Errorf("opening block = %q", open)
	}
}

func TestContinueValue(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"split \\", "split \n"},
		{"split \\  ", "split \n"},
		{`\`, "\n"},
		{"a\\\\", "a\\\n"}, // an escaped backslash keeps one, the second continues
	} {
		if got := continueValue(tc.in); got != tc.want {
			t.Errorf("continueValue(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEnterWithTrailingBackslashContinuesThenSubmits(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{llm.StreamEnd{}}}
	m, rec := newTestModel(fake)
	m.input.SetValue("split \\")
	mm, _ := m.Update(enter())
	m = mm.(Model)
	if fake.calls.Load() != 0 {
		t.Fatalf("continuation Enter must not start a stream, calls = %d", fake.calls.Load())
	}
	if m.live != nil {
		t.Fatal("no stream should be live after a continuation")
	}
	if got, want := m.input.Value(), "split \n"; got != want {
		t.Errorf("after continuation input = %q, want %q", got, want)
	}
	// Enter again submits the accumulated lines, trailing newline trimmed.
	m = drive(t, m, enter())
	if fake.calls.Load() != 1 {
		t.Errorf("second Enter should submit, calls = %d", fake.calls.Load())
	}
	if user := ansi.Strip(rec.chunks[1]); !strings.Contains(user, "split") {
		t.Errorf("user bubble chunk = %q, want 'split'", user)
	}
}

func TestEnterWithTrailingBackslashAndSpacesContinues(t *testing.T) {
	m, _ := newTestModel(&fakeProvider{})
	m.input.SetValue("foo \\  ") // backslash then two spaces
	mm, _ := m.Update(enter())
	m = mm.(Model)
	if got, want := m.input.Value(), "foo \n"; got != want {
		t.Errorf("after continuation input = %q, got %q", got, want)
	}
}

func TestEnterSubmitsWhenCursorNotAtEnd(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{llm.StreamEnd{}}}
	m, rec := newTestModel(fake)
	m.input.SetValue("a \\")
	m.input.CursorStart()
	m = drive(t, m, enter())
	if fake.calls.Load() != 1 {
		t.Fatalf("Enter from mid-line should submit, calls = %d", fake.calls.Load())
	}
	if user := ansi.Strip(rec.chunks[1]); !strings.Contains(user, "a \\") {
		t.Errorf("user bubble chunk = %q, want 'a \\'", user)
	}
}

func TestInputHeightTracksContent(t *testing.T) {
	m, _ := newTestModel(&fakeProvider{})
	if got := m.input.Height(); got != 1 {
		t.Errorf("empty input height = %d, want 1", got)
	}
	m.input.SetValue("1\n2\n3")
	if got := m.input.Height(); got != 3 {
		t.Errorf("input height with 3 lines = %d, want 3", got)
	}
	m.input.SetValue(strings.Repeat("idle\n", 30)) // well past MaxHeight
	if got := m.input.Height(); got != 8 {
		t.Errorf("input height past MaxHeight = %d, want capped at 8", got)
	}
	m.input.Reset()
	if got := m.input.Height(); got != 1 {
		t.Errorf("input height after reset = %d, want 1", got)
	}
}

func TestInputStylesHaveNoBackground(t *testing.T) {
	m, _ := newTestModel(&fakeProvider{})
	noBg := lipgloss.NoColor{}
	s := m.input.Styles()
	for _, st := range []struct {
		name  string
		style lipgloss.Style
	}{
		{"focused base", s.Focused.Base},
		{"focused text", s.Focused.Text},
		{"focused cursorline", s.Focused.CursorLine},
		{"focused placeholder", s.Focused.Placeholder},
		{"focused endofbuffer", s.Focused.EndOfBuffer},
		{"blurred base", s.Blurred.Base},
		{"blurred text", s.Blurred.Text},
		{"blurred cursorline", s.Blurred.CursorLine},
	} {
		if bg := st.style.GetBackground(); bg != noBg {
			t.Errorf("%s background = %v, want none", st.name, bg)
		}
	}
	// Selection is the one deliberate exception — highlighted text must stay
	// legible, and every other background is what the user complained about.
	if bg := s.Focused.Selection.GetBackground(); bg == noBg {
		t.Error("selection should keep a background for legibility")
	}
}

// fakeTool is a registered stand-in with a configurable category, used to
// verify plan-mode tool filtering (only read tools offered).
type fakeTool struct {
	name string
	cat  tools.Category
}

func (t fakeTool) Name() string             { return t.name }
func (t fakeTool) Description() string      { return "fake" }
func (t fakeTool) Category() tools.Category { return t.cat }
func (t fakeTool) Schema() tools.ToolSchema { return tools.ToolSchema{Name: t.name} }
func (t fakeTool) Execute(context.Context, map[string]any) tools.ToolResult {
	return tools.ToolResult{Output: "ok"}
}

// startBlockedLive opens a live turn against a blocking provider and returns
// the model with m.live != nil (the waitStream cmd is not run).
func startBlockedLive(t *testing.T) (Model, *printRecorder) {
	t.Helper()
	fake := &fakeProvider{block: true}
	m, rec := newTestModel(fake)
	m.input.SetValue("q")
	mm, _ := m.Update(enter())
	m = mm.(Model)
	if m.live == nil {
		t.Fatal("expected live stream after enter")
	}
	return m, rec
}

func TestPlanDoSwitchModes(t *testing.T) {
	conv := conversation.NewManager()
	reg := tools.NewRegistry()
	reg.Register(fakeTool{name: "read_tool", cat: tools.CategoryRead})
	reg.Register(fakeTool{name: "write_tool", cat: tools.CategoryWrite})
	fake := &fakeProvider{scripts: [][]llm.StreamEvent{
		{llm.TextDelta{Text: "plan"}, llm.StreamEnd{}},
	}}
	ag := agent.New(fake, reg, conv, prompt.System, false, config.DefaultMaxIterations)
	m := newModel(Options{Agent: ag, Conv: conv, Registry: reg, ProviderName: "fake", ModelName: "m", Version: "test", WorkDir: "/tmp"})
	rec := &printRecorder{}
	m.print = rec.record
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = mm.(Model)

	m = send(t, m, "/plan")
	if ag.Mode() != agent.ModePlan {
		t.Fatalf("mode = %s, want plan", ag.Mode())
	}
	if last := ansi.Strip(rec.lastChunk()); !strings.Contains(last, "Plan mode on") {
		t.Errorf("/plan notice = %q", last)
	}
	if status := ansi.Strip(m.renderStatus()); !strings.Contains(status, "[plan]") {
		t.Errorf("status bar should show [plan] in plan mode: %q", status)
	}

	// The next request carries the plan segment and only read tools.
	m = send(t, m, "draft a plan")
	req := fake.lastReq
	if !strings.Contains(req.System, "PLAN MODE") || !strings.HasPrefix(req.System, prompt.System) {
		t.Errorf("plan-mode system prompt = %q", req.System)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "read_tool" {
		t.Errorf("plan-mode tools = %+v, want only the read tool", req.Tools)
	}

	// /do switches back: base prompt, full tool set.
	m = send(t, m, "/do")
	if ag.Mode() != agent.ModeNormal {
		t.Fatalf("mode = %s, want normal after /do", ag.Mode())
	}
	if status := ansi.Strip(m.renderStatus()); strings.Contains(status, "[plan]") {
		t.Errorf("status bar should drop [plan] in normal mode: %q", status)
	}
	m = send(t, m, "execute it")
	req = fake.lastReq
	if strings.Contains(req.System, "PLAN MODE") {
		t.Errorf("normal-mode system prompt carries the plan segment: %q", req.System)
	}
	if len(req.Tools) != 2 {
		t.Errorf("normal-mode tools = %+v, want both", req.Tools)
	}
}

func TestEscCancelsLiveTurn(t *testing.T) {
	m, _ := startBlockedLive(t)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.live == nil {
		t.Fatal("esc must not close the live turn itself; the Cancelled event does")
	}
	if m.live.ctx.Err() == nil {
		t.Error("esc must cancel the loop context")
	}
	// While still live, commands are ignored — mode must not change.
	m.input.SetValue("/plan")
	mm, _ = m.Update(enter())
	m = mm.(Model)
	if m.opts.Agent.Mode() != agent.ModeNormal {
		t.Errorf("mode changed while live: %s", m.opts.Agent.Mode())
	}
	m.live.cancel()
}

func TestEscIdleIsNoop(t *testing.T) {
	m, _ := newTestModel(&fakeProvider{})
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.live != nil {
		t.Error("esc while idle must not open a stream")
	}
}

func TestToolCallEndPairsByToolID(t *testing.T) {
	// Two same-name calls in flight; ends arrive in call order. Name-based
	// pairing would swap the results; ToolID pairing must not.
	m, rec := startBlockedLive(t)
	mm, _ := m.Update(streamBatchMsg{
		agent.ToolCallBegin{ToolID: "tu_1", ToolName: "echo_tool", Args: map[string]any{}},
		agent.ToolCallBegin{ToolID: "tu_2", ToolName: "echo_tool", Args: map[string]any{}},
	})
	m = mm.(Model)
	if len(m.running) != 2 || m.running[0].id != "tu_1" || m.running[1].id != "tu_2" {
		t.Fatalf("running = %+v, want tu_1 then tu_2", m.running)
	}
	mm, _ = m.Update(streamBatchMsg{
		agent.ToolCallEnd{ToolID: "tu_1", ToolName: "echo_tool", Result: tools.ToolResult{Output: "first", IsError: true}},
		agent.ToolCallEnd{ToolID: "tu_2", ToolName: "echo_tool", Result: tools.ToolResult{Output: "second"}},
	})
	m = mm.(Model)
	if len(m.running) != 0 {
		t.Errorf("running after ends = %+v, want empty", m.running)
	}
	// The failure marker must print before the success marker.
	raw := rec.lastChunk()
	failed, done := strings.Index(raw, styleToolFailed.Render("●")), strings.Index(raw, styleToolDone.Render("●"))
	if failed < 0 || done < 0 || failed > done {
		t.Errorf("settled tool blocks missing or out of order:\n%q", raw)
	}
	m.live.cancel()
}

// foldTerminal opens a live turn and folds one terminal event into it.
func foldTerminal(t *testing.T, term agent.AgentEvent) (Model, *printRecorder) {
	t.Helper()
	m, rec := startBlockedLive(t)
	mm, _ := m.Update(streamBatchMsg{term})
	return mm.(Model), rec
}

func TestLoopCompleteStopReasons(t *testing.T) {
	// ModelStop: the plain done footer.
	m, rec := foldTerminal(t, agent.LoopComplete{Reason: agent.StoppedByModel, TotalTurns: 2})
	if m.live != nil {
		t.Fatal("terminal event must close the live turn")
	}
	if last := ansi.Strip(rec.lastChunk()); !strings.Contains(last, "· done") {
		t.Errorf("ModelStop last chunk = %q, want turn footer", last)
	}

	// MaxIterations: yellow guard-rail notice, then the footer.
	_, rec = foldTerminal(t, agent.LoopComplete{Reason: agent.StoppedByLimit, TotalTurns: 50})
	last := ansi.Strip(rec.lastChunk())
	if !strings.Contains(last, "Turn limit reached") || !strings.Contains(last, "· done") {
		t.Errorf("MaxIterations chunk = %q, want warn notice + footer", last)
	}

	// Cancelled: a neutral notice and no footer.
	_, rec = foldTerminal(t, agent.LoopComplete{Reason: agent.StoppedByUser})
	last = ansi.Strip(rec.lastChunk())
	if !strings.Contains(last, "Cancelled.") || strings.Contains(last, "· done") {
		t.Errorf("Cancelled chunk = %q, want neutral notice only", last)
	}
}

func TestTurnCompleteTracksLiveTurnCounter(t *testing.T) {
	m, _ := startBlockedLive(t)
	mm, _ := m.Update(streamBatchMsg{agent.TurnComplete{Turn: 3}})
	m = mm.(Model)
	if m.live == nil || m.live.turn != 3 {
		t.Fatalf("live turn = %d, want 3", m.live.turn)
	}
	// The spinner line shows the turn number once one has completed.
	if !strings.Contains(ansi.Strip(m.View().Content), "Turn 3") {
		t.Error("live line should show the current turn")
	}
	m.live.cancel()

	// Turn 0 (pure reply, no tool round yet) hides the turn part.
	m, _ = startBlockedLive(t)
	if strings.Contains(ansi.Strip(m.View().Content), "Turn") {
		t.Error("turn counter must stay hidden before the first TurnComplete")
	}
	m.live.cancel()
}
