package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/charmbracelet/x/ansi"
)

// freeze the clock so lifecycle timing (elapsed, doneAt) is deterministic.
func init() {
	timeNow = func() time.Time { return time.Unix(1000, 0) }
	timeSince = func(time.Time) time.Duration { return 7 * time.Second }
}

func TestCachedRenderMemoizesPerWidth(t *testing.T) {
	e := newAssistantEntry("x", 0, timeNow())
	calls := 0
	render := func(md string) string { calls++; return "<" + md + ">" }
	if got := e.cachedRender(80, render); got != "<x>" || calls != 1 {
		t.Fatalf("first = %q calls=%d", got, calls)
	}
	if got := e.cachedRender(80, render); got != "<x>" || calls != 1 {
		t.Fatalf("cached = %q calls=%d", got, calls)
	}
	if got := e.cachedRender(40, render); got != "<x>" || calls != 2 {
		t.Fatalf("after width change = %q calls=%d", got, calls)
	}
}

func TestWholeTurnCommitOnStreamEnd(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{
		llm.TextDelta{Text: "a"}, llm.TextDelta{Text: "b"}, llm.StreamEnd{},
	}}
	m := send(t, newTestModel(fake), "q")
	last := lastEntry(t, m)
	if last.kind != entryAssistant || last.text != "ab" {
		t.Fatalf("last entry = %+v, want assistant 'ab'", last)
	}
	if last.elapsed != 7*time.Second || last.doneAt.IsZero() {
		t.Fatalf("footer timing not set: %+v", last)
	}
	if m.conv.Len() != 2 {
		t.Fatalf("conv.Len = %d, want 2", m.conv.Len())
	}
	if m.live != nil {
		t.Fatal("stream should be idle after Done")
	}
	chat := ansi.Strip(renderTop(&m))
	if !strings.Contains(chat, "ab") || !strings.Contains(chat, "7s · done") {
		t.Errorf("committed reply should render with footer:\n%s", chat)
	}
}

func TestErrorDoesNotCommitHistory(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{llm.StreamError{Err: errors.New("boom")}}}
	m := send(t, newTestModel(fake), "q")
	if m.conv.Len() != 0 {
		t.Fatalf("conv.Len = %d, want 0", m.conv.Len())
	}
	last := lastEntry(t, m)
	if last.kind != entryNotice || !last.isError {
		t.Fatalf("last entry = %+v, want error notice", last)
	}
	if !strings.Contains(ansi.Strip(renderTop(&m)), "Error: boom") {
		t.Error("error notice should render")
	}
}

func TestThinkingAndToolEventsIgnored(t *testing.T) {
	fake := &fakeProvider{events: []llm.StreamEvent{
		llm.TextDelta{Text: "visible"},
		llm.ThinkingDelta{Text: "hidden-thinking"},
		llm.ToolCallStart{ToolID: "1", ToolName: "bash"},
		llm.ToolCallComplete{ToolID: "1", ToolName: "bash", Arguments: map[string]any{"x": 1}},
		llm.TextDelta{Text: "!"},
		llm.StreamEnd{},
	}}
	m := send(t, newTestModel(fake), "q")
	last := lastEntry(t, m)
	if last.text != "visible!" {
		t.Fatalf("assistant text = %q, thinking/tool must be ignored", last.text)
	}
	if strings.Contains(ansi.Strip(renderTop(&m)), "hidden-thinking") {
		t.Error("thinking must not render")
	}
}

// lastEntry returns the most recent committed entry.
func lastEntry(t *testing.T, m Model) *entry {
	t.Helper()
	if len(m.entries) == 0 {
		t.Fatal("no entries")
	}
	return &m.entries[len(m.entries)-1]
}
