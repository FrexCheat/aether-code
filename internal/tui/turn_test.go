package tui

import (
	"errors"
	"testing"
	"time"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
)

func turnOf(t *testing.T, ts *Transcript, idx int) *Turn {
	t.Helper()
	turn, ok := ts.At(idx).(*Turn)
	if !ok {
		t.Fatalf("item at %d is not a Turn", idx)
	}
	return turn
}

func TestTextDeltaExtendsOpenTextPart(t *testing.T) {
	ts := &Transcript{}
	idx := openTurn(ts, "q")
	applyEvent(ts, idx, llm.TextDelta{Text: "a"}, conversation.NewManager())
	applyEvent(ts, idx, llm.TextDelta{Text: "b"}, conversation.NewManager())
	turn := turnOf(t, ts, idx)
	if len(turn.Parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(turn.Parts))
	}
	if tp := turn.Parts[0].(*TextPart); tp.Text != "ab" {
		t.Errorf("text = %q, want %q", tp.Text, "ab")
	}
}

func TestNonTextEventSealsTextPart(t *testing.T) {
	ts := &Transcript{}
	idx := openTurn(ts, "q")
	applyEvent(ts, idx, llm.TextDelta{Text: "one"}, conversation.NewManager())
	applyEvent(ts, idx, llm.ThinkingDelta{Text: "hmm"}, conversation.NewManager())
	applyEvent(ts, idx, llm.TextDelta{Text: "two"}, conversation.NewManager())
	turn := turnOf(t, ts, idx)
	if len(turn.Parts) != 3 {
		t.Fatalf("parts = %d, want 3 (text, thinking, text)", len(turn.Parts))
	}
	if turn.Parts[0].(*TextPart).Text != "one" {
		t.Errorf("part0 = %q", turn.Parts[0].(*TextPart).Text)
	}
	if turn.Parts[1].(*ThinkingPart).Text != "hmm" {
		t.Errorf("part1 = %q", turn.Parts[1].(*ThinkingPart).Text)
	}
	if turn.Parts[2].(*TextPart).Text != "two" {
		t.Errorf("part2 = %q", turn.Parts[2].(*TextPart).Text)
	}
}

func TestToolDeltaAddressing(t *testing.T) {
	ts := &Transcript{}
	idx := openTurn(ts, "q")
	applyEvent(ts, idx, llm.ToolCallStart{ToolID: "a", ToolName: "bash"}, conversation.NewManager())
	applyEvent(ts, idx, llm.ToolCallStart{ToolID: "b", ToolName: "read"}, conversation.NewManager())
	applyEvent(ts, idx, llm.ToolCallComplete{ToolID: "b", ToolName: "read", Arguments: map[string]any{"path": "x"}}, conversation.NewManager())
	turn := turnOf(t, ts, idx)
	if len(turn.Parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(turn.Parts))
	}
	a, b := turn.Parts[0].(*ToolPart), turn.Parts[1].(*ToolPart)
	if a.ToolID != "a" || a.done {
		t.Errorf("tool a = %+v, want open", a)
	}
	if b.ToolID != "b" || !b.done || b.Arguments["path"] != "x" {
		t.Errorf("tool b = %+v", b)
	}
}

func TestCloseTurnCommitsOnlyOnSuccess(t *testing.T) {
	ts := &Transcript{}
	idx := openTurn(ts, "q1")
	applyEvent(ts, idx, llm.TextDelta{Text: "ab"}, conversation.NewManager())
	applyEvent(ts, idx, llm.StreamError{Err: errors.New("boom")}, conversation.NewManager())
	turn := turnOf(t, ts, idx)
	if turn.Status != TurnFailed || turn.Err == nil {
		t.Fatalf("turn = %+v", turn)
	}

	conv := conversation.NewManager()
	idx = openTurn(ts, "q2")
	applyEvent(ts, idx, llm.TextDelta{Text: "a"}, conv)
	applyEvent(ts, idx, llm.TextDelta{Text: "b"}, conv)
	applyEvent(ts, idx, llm.StreamEnd{}, conv)
	turn = turnOf(t, ts, idx)
	if turn.Status != TurnDone || turn.Elapsed < 0 || turn.DoneAt.IsZero() {
		t.Fatalf("turn = %+v", turn)
	}
	if conv.Len() != 2 {
		t.Fatalf("conv.Len = %d, want 2", conv.Len())
	}
	msgs := conv.GetMessages()
	if msgs[0].Content != "q2" || msgs[1].Content != "ab" {
		t.Errorf("messages = %+v", msgs)
	}
}

func TestCachedRenderMemoizesPerWidth(t *testing.T) {
	p := &TextPart{Text: "x", done: true}
	calls := 0
	render := func(md string) string { calls++; return "<" + md + ">" }
	if got := p.cachedRender(80, render); got != "<x>" || calls != 1 {
		t.Fatalf("first = %q calls=%d", got, calls)
	}
	if got := p.cachedRender(80, render); got != "<x>" || calls != 1 {
		t.Fatalf("cached = %q calls=%d", got, calls)
	}
	if got := p.cachedRender(40, render); got != "<x>" || calls != 2 {
		t.Fatalf("after width change = %q calls=%d", got, calls)
	}
}

func TestWaitStreamCoalescesBurst(t *testing.T) {
	ch := make(chan llm.StreamEvent, 4)
	ch <- llm.TextDelta{Text: "a"}
	ch <- llm.TextDelta{Text: "b"}
	ch <- llm.TextDelta{Text: "c"}
	close(ch)
	msg := waitStream(ch)()
	batch, ok := msg.(streamBatchMsg)
	if !ok {
		t.Fatalf("msg = %T, want streamBatchMsg", msg)
	}
	if len(batch) != 3 {
		t.Fatalf("batch = %d events, want 3 (one Update for the whole burst)", len(batch))
	}
}

func TestVisibleLines(t *testing.T) {
	if got := visibleLines("partial"); got != "" {
		t.Errorf("partial line visible: %q", got)
	}
	if got := visibleLines("one\ntwo"); got != "one\n" {
		t.Errorf("visible = %q, want %q", got, "one\n")
	}
	if got := visibleLines("one\n"); got != "one\n" {
		t.Errorf("visible = %q", got)
	}
}

// freeze clock helpers so lifecycle timing is deterministic in tests.
func init() {
	timeNow = func() time.Time { return time.Unix(1000, 0) }
	timeSince = func(time.Time) time.Duration { return 7 * time.Second }
}
