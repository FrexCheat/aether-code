package tui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/prompt"
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
	m := newModel(Options{Provider: p, ProviderName: "fake", ModelName: "fake-1", Version: "test", WorkDir: "/tmp"})
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return mm.(Model)
}

// drive feeds msg to Update, then executes returned commands until none remain.
// tickMsg results are not re-fed, so the loop terminates once the stream ends.
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
	case tickMsg:
		return nil
	}
	return []tea.Msg{msg}
}

func enter() tea.Msg { return tea.KeyPressMsg{Code: tea.KeyEnter} }

func send(t *testing.T, m Model, text string) Model {
	t.Helper()
	m.input.SetValue(text)
	return drive(t, m, enter())
}

func TestKeyEnterString(t *testing.T) {
	if s := (tea.KeyPressMsg{Code: tea.KeyEnter}).String(); s != "enter" {
		t.Fatalf("enter key String() = %q", s)
	}
}

func TestSendCommitsOnDone(t *testing.T) {
	tickInterval = time.Millisecond
	fake := &fakeProvider{events: []llm.StreamEvent{llm.TextDelta{Text: "Hi"}, llm.StreamEnd{}}}
	m := send(t, newTestModel(fake), "hello")
	if m.conv.Len() != 2 {
		t.Errorf("conv.Len = %d, want 2", m.conv.Len())
	}
	if len(m.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(m.entries))
	}
	last := m.entries[1]
	if !last.done || last.err != nil || last.text != "Hi" {
		t.Errorf("last entry = %+v", last)
	}
	if m.streaming {
		t.Error("streaming should be false after Done")
	}
	if len(fake.lastReq.Messages) != 1 || fake.lastReq.System != prompt.System {
		t.Errorf("request = %+v", fake.lastReq)
	}
	if !strings.Contains(m.renderChat(), "Hi") || !strings.Contains(m.renderChat(), "· done") {
		t.Errorf("chat should show text and elapsed:\n%s", m.renderChat())
	}
}

func TestSendDoesNotCommitOnError(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{llm.StreamError{Err: errors.New("boom")}}}
	m := send(t, newTestModel(fake), "hello")
	if m.conv.Len() != 0 {
		t.Errorf("conv.Len = %d, want 0", m.conv.Len())
	}
	last := m.entries[len(m.entries)-1]
	if last.err == nil || !strings.Contains(last.err.Error(), "boom") {
		t.Errorf("last entry = %+v", last)
	}
	if m.streaming {
		t.Error("streaming should be false after Error")
	}
	if !strings.Contains(m.renderChat(), "Error: boom") {
		t.Errorf("chat should show error:\n%s", m.renderChat())
	}
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
		t.Errorf("after /clear: Len=%d entries=%d", m.conv.Len(), len(m.entries))
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
	last := m.entries[len(m.entries)-1]
	if last.err == nil || !strings.Contains(last.err.Error(), "Unknown command") {
		t.Errorf("last entry = %+v", last)
	}
}

func TestEnterIgnoredWhileStreaming(t *testing.T) {
	fake := &fakeProvider{block: true}
	m := newTestModel(fake)
	m.input.SetValue("first")
	mm, _ := m.Update(enter()) // do not run the blocking waitForEvent cmd
	m = mm.(Model)
	if !m.streaming {
		t.Fatal("expected streaming after first enter")
	}
	if !strings.Contains(m.renderChat(), "Imagining…") {
		t.Errorf("chat should show progress indicator:\n%s", m.renderChat())
	}
	m.input.SetValue("second")
	mm, _ = m.Update(enter())
	m = mm.(Model)
	if fake.calls.Load() != 1 {
		t.Errorf("Stream calls = %d, want 1", fake.calls.Load())
	}
	if m.cancel != nil {
		m.cancel()
	}
}

func TestViewContainsAllRegions(t *testing.T) {
	m := newTestModel(&fakeProvider{})
	v := ansi.Strip(m.View().Content)
	for _, want := range []string{"AetherCode", "vtest", "/tmp", "Ready.", "Send a message...", "fake", "fake-1"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q", want)
		}
	}
	if !m.View().AltScreen {
		t.Error("AltScreen should be enabled")
	}
}
