package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/spinner"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/prompt"
	"github.com/FrexCheat/aether-code/internal/tools"
)

// fakeProvider replays a fixed event list and records the last request.
type fakeProvider struct {
	events  []llm.StreamEvent
	block   bool // if true, never send or close (simulates an in-flight stream)
	calls   atomic.Int32
	lastReq llm.Request
}

func (f *fakeProvider) Stream(ctx context.Context, req llm.Request) <-chan llm.StreamEvent {
	f.calls.Add(1)
	f.lastReq = req
	ch := make(chan llm.StreamEvent, len(f.events)+1)
	if f.block {
		return ch
	}
	for _, ev := range f.events {
		ch <- ev
	}
	close(ch)
	return ch
}

func newTestModel(p llm.Client) Model {
	conv := conversation.NewManager()
	ag := agent.New(p, tools.NewRegistry(), conv, prompt.System, false)
	m := newModel(Options{
		Agent: ag, Conv: conv, ProviderName: "fake", ModelName: "fake-1",
		Version: "test", WorkDir: "/tmp",
	})
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return mm.(Model)
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
	m := send(t, newTestModel(fake), "hello")
	if m.conv.Len() != 2 {
		t.Errorf("conv.Len = %d, want 2", m.conv.Len())
	}
	if len(m.entries) != 3 { // user + assistant + turn footer
		t.Fatalf("entries = %d, want 3", len(m.entries))
	}
	reply := m.entries[len(m.entries)-2]
	if reply.kind != entryAssistant || reply.text != "Hi" {
		t.Errorf("assistant entry = %+v", reply)
	}
	if m.live != nil {
		t.Error("stream should be idle after Done")
	}
	if len(fake.lastReq.Messages) != 1 || fake.lastReq.System != prompt.System {
		t.Errorf("request = %+v", fake.lastReq)
	}
	chat := ansi.Strip(renderTop(&m))
	if !strings.Contains(chat, "hello") || !strings.Contains(chat, "Hi") || !strings.Contains(chat, "· done") {
		t.Errorf("chat should show question, reply and turn footer:\n%s", chat)
	}
}

func TestStreamingTextHiddenUntilCommit(t *testing.T) {
	fake := &fakeProvider{block: true}
	m := newTestModel(fake)
	m.input.SetValue("q")
	mm, _ := m.Update(enter()) // start the turn; discard the blocking waitStream cmd
	m = mm.(Model)
	if m.live == nil {
		t.Fatal("expected live stream after enter")
	}
	// Fold a text-only batch (no terminal event): text accumulates but is hidden.
	mm, _ = m.Update(streamBatchMsg([]agent.AgentEvent{agent.TextDelta{Text: "secret"}}))
	m = mm.(Model)
	chat := ansi.Strip(renderTop(&m))
	if strings.Contains(chat, "secret") {
		t.Errorf("in-flight reply text must stay hidden:\n%s", chat)
	}
	if !strings.Contains(chat, "Imagining") {
		t.Errorf("chat should show loading indicator:\n%s", chat)
	}
	m.live.cancel()
}

func TestSpinnerAdvancesOnTick(t *testing.T) {
	fake := &fakeProvider{block: true}
	m := newTestModel(fake)
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
	if !strings.Contains(renderTop(&m), "Imagining") {
		t.Error("live render should include the loading indicator")
	}
	m.live.cancel()
}

func TestSecondTurnCarriesHistory(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{llm.TextDelta{Text: "A"}, llm.StreamEnd{}}}
	m := send(t, newTestModel(fake), "q1")
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
	m := send(t, newTestModel(fake), "q1")
	m = send(t, m, "/clear")
	if m.conv.Len() != 0 || len(m.entries) != 0 {
		t.Errorf("after /clear: conv=%d entries=%d", m.conv.Len(), len(m.entries))
	}
	if fake.calls.Load() != 1 {
		t.Errorf("/clear must not call Stream, calls = %d", fake.calls.Load())
	}
}

func TestUnknownCommandNoStream(t *testing.T) {
	fake := &fakeProvider{}
	m := send(t, newTestModel(fake), "/foo")
	if fake.calls.Load() != 0 {
		t.Errorf("Stream called %d times", fake.calls.Load())
	}
	if len(m.entries) != 1 {
		t.Fatalf("entries = %d, want 1 notice", len(m.entries))
	}
	last := lastEntry(t, m)
	if last.kind != entryNotice || !strings.Contains(last.text, "Unknown command") {
		t.Errorf("notice = %+v", last)
	}
}

func TestEnterIgnoredWhileStreaming(t *testing.T) {
	fake := &fakeProvider{block: true}
	m := newTestModel(fake)
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
	m := newTestModel(&fakeProvider{})
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

func TestViewMainScreenAndRegions(t *testing.T) {
	m := newTestModel(&fakeProvider{})
	v := ansi.Strip(m.View().Content)
	for _, want := range []string{"AetherCode", "Ready.", "fake", "fake-1"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q", want)
		}
	}
	if m.View().AltScreen {
		t.Error("AltScreen should be disabled for native copy")
	}
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Error("MouseMode should be CellMotion for wheel scrolling")
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
	m := newTestModel(fake)
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
	if len(m.entries) != 2 || m.entries[0].kind != entryUser || m.entries[0].text != "split" {
		t.Errorf("first entry = %+v, want user 'split'", m.entries[0])
	}
}

func TestEnterWithTrailingBackslashAndSpacesContinues(t *testing.T) {
	m := newTestModel(&fakeProvider{})
	m.input.SetValue("foo \\  ") // backslash then two spaces
	mm, _ := m.Update(enter())
	m = mm.(Model)
	if got, want := m.input.Value(), "foo \n"; got != want {
		t.Errorf("after continuation input = %q, want %q", got, want)
	}
}

func TestEnterSubmitsWhenCursorNotAtEnd(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{llm.StreamEnd{}}}
	m := newTestModel(fake)
	m.input.SetValue("a \\")
	m.input.CursorStart()
	m = drive(t, m, enter())
	if fake.calls.Load() != 1 {
		t.Fatalf("Enter from mid-line should submit, calls = %d", fake.calls.Load())
	}
	if len(m.entries) != 2 || m.entries[0].kind != entryUser || m.entries[0].text != "a \\" {
		t.Errorf("first entry = %+v, want user 'a \\'", m.entries[0])
	}
}

func TestInputHeightTracksContent(t *testing.T) {
	m := newTestModel(&fakeProvider{})
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

func TestViewportLayoutSumsToWindowHeight(t *testing.T) {
	m := newTestModel(&fakeProvider{}) // 80×24 window
	for lines := 1; lines <= 6; lines++ {
		m.input.SetValue(strings.Repeat("line\n", lines-1) + "line")
		mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		m = mm.(Model)
		total := m.viewport.Height() + inputViewHeight(&m) + statusBarHeight
		if total != 24 {
			t.Errorf("lines=%d: layout total = %d, want 24 (vp=%d input=%d)",
				lines, total, m.viewport.Height(), inputViewHeight(&m))
		}
	}
}

func TestInputStylesHaveNoBackground(t *testing.T) {
	m := newTestModel(&fakeProvider{})
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
