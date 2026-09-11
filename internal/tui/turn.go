package tui

import (
	"time"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
)

// TurnStatus is the lifecycle state of a Turn. A turn starts streaming, then
// settles into exactly one terminal state.
type TurnStatus int

const (
	TurnStreaming TurnStatus = iota // response in flight
	TurnDone                        // StreamEnd received
	TurnFailed                      // StreamError received
)

// NoticeSeverity classifies a system notice for styling.
type NoticeSeverity int

const (
	SeverityInfo NoticeSeverity = iota
	SeverityError
)

// Item is the sealed set of transcript entries: a question-answer Turn or a
// system Notice. Consumers must use a type switch.
type Item interface{ isItem() }

// Turn is one question-answer round: the user's question, the response parts
// in arrival order, and the lifecycle state with timing. Err carries the
// stream error when Status is TurnFailed.
type Turn struct {
	Question string
	Parts    []Part
	Status   TurnStatus
	Started  time.Time
	Elapsed  time.Duration
	DoneAt   time.Time
	Err      error
}

// Notice is a system message (e.g. unknown-command hints). It is its own
// item type so error styling never leaks into assistant turns.
type Notice struct {
	Text     string
	Severity NoticeSeverity
}

// Part is the sealed set of response segments inside a Turn, mirroring the
// llm.StreamEvent vocabulary. Consumers must use a type switch.
type Part interface{ isPart() }

// TextPart accumulates plain-text deltas. rendered is the private cache of
// the finished whole-part markdown render, keyed by width: it is only
// populated when the part is sealed and invalidated on width change. While
// streaming, only complete lines are shown and the cache stays empty.
type TextPart struct {
	Text     string
	width    int
	rendered string
	done     bool
}

// ThinkingPart accumulates thinking deltas for a later collapsed display.
// This phase only stores; rendering arrives in a later phase.
type ThinkingPart struct {
	Text      string
	collapsed bool
}

// ToolPart accumulates one tool call addressed by ToolID so interleaved
// deltas from parallel calls land on the right part. Arguments holds the
// parsed result once ToolCallComplete arrives. Rendering arrives later.
type ToolPart struct {
	ToolID    string
	ToolName  string
	ArgsText  string
	Arguments map[string]any
	done      bool
}

func (*Turn) isItem()   {}
func (*Notice) isItem() {}

func (*TextPart) isPart()     {}
func (*ThinkingPart) isPart() {}
func (*ToolPart) isPart()     {}

// Transcript is the append-only screen record: every Turn and Notice in
// display order. Entries are pointers so mutation of a live turn is explicit.
type Transcript struct {
	items []Item
}

// Append adds an item and returns its index.
func (t *Transcript) Append(it Item) int {
	t.items = append(t.items, it)
	return len(t.items) - 1
}

// Items returns the entries in display order. The slice is a copy; the
// entries themselves are shared.
func (t *Transcript) Items() []Item {
	out := make([]Item, len(t.items))
	copy(out, t.items)
	return out
}

// At returns the entry at idx or nil when out of range.
func (t *Transcript) At(idx int) Item {
	if idx < 0 || idx >= len(t.items) {
		return nil
	}
	return t.items[idx]
}

// Len returns the number of entries.
func (t *Transcript) Len() int { return len(t.items) }

// Reset discards all entries.
func (t *Transcript) Reset() { t.items = nil }

// openTurn appends a new streaming Turn for question and returns its index.
func openTurn(t *Transcript, question string) int {
	return t.Append(&Turn{Question: question, Status: TurnStreaming, Started: timeNow()})
}

// applyEvent folds one stream event into the turn at idx. Non-text segment
// events seal the currently open text part first so part order matches
// arrival order. Terminal events delegate to closeTurn.
func applyEvent(t *Transcript, idx int, ev llm.StreamEvent, conv *conversation.Manager) {
	turn, ok := t.At(idx).(*Turn)
	if !ok {
		return
	}
	switch e := ev.(type) {
	case llm.TextDelta:
		openText(turn).Text += e.Text
	case llm.ThinkingDelta:
		sealText(turn)
		openThinking(turn).Text += e.Text
	case llm.ThinkingComplete:
		sealText(turn)
		tp := openThinking(turn)
		tp.Text = e.Thinking
	case llm.ToolCallStart:
		sealText(turn)
		turn.Parts = append(turn.Parts, &ToolPart{ToolID: e.ToolID, ToolName: e.ToolName})
	case llm.ToolCallDelta:
		sealText(turn)
		// llm.ToolCallDelta carries no id yet, so parallel in-flight calls
		// cannot be disambiguated here; deltas land on the trailing open
		// part until the event grows an id (spec: llm 层不动).
		if tp := toolPart(turn, ""); tp != nil {
			tp.ArgsText += e.Text
		}
	case llm.ToolCallComplete:
		sealText(turn)
		if tp := toolPart(turn, e.ToolID); tp != nil {
			tp.ToolName = e.ToolName
			tp.Arguments = e.Arguments
			tp.done = true
		}
	case llm.StreamEnd, llm.StreamError:
		closeTurn(t, idx, ev, conv)
	}
}

// closeTurn settles the turn at idx: StreamEnd seals the parts, records
// timing and commits question + text to history; StreamError only marks the
// turn failed and leaves history untouched. This is the only place that
// writes to the conversation history.
func closeTurn(t *Transcript, idx int, ev llm.StreamEvent, conv *conversation.Manager) {
	turn, ok := t.At(idx).(*Turn)
	if !ok || turn.Status != TurnStreaming {
		return
	}
	switch ev := ev.(type) {
	case llm.StreamEnd:
		for _, p := range turn.Parts {
			switch p := p.(type) {
			case *TextPart:
				p.done = true
			case *ToolPart:
				p.done = true
			}
		}
		turn.Elapsed = timeSince(turn.Started)
		turn.DoneAt = timeNow()
		turn.Status = TurnDone
		conv.AddUserMessage(turn.Question)
		conv.AddAssistantMessage(turnText(turn))
	case llm.StreamError:
		turn.Err = ev.Err
		turn.Status = TurnFailed
	}
}

// openText returns the trailing open TextPart, creating one when needed.
func openText(turn *Turn) *TextPart {
	if n := len(turn.Parts); n > 0 {
		if tp, ok := turn.Parts[n-1].(*TextPart); ok && !tp.done {
			return tp
		}
	}
	tp := &TextPart{}
	turn.Parts = append(turn.Parts, tp)
	return tp
}

// sealText marks the trailing TextPart done so the next segment starts fresh.
func sealText(turn *Turn) {
	if n := len(turn.Parts); n > 0 {
		if tp, ok := turn.Parts[n-1].(*TextPart); ok {
			tp.done = true
		}
	}
}

// cachedRender returns the whole-part markdown render memoized per width.
// Only sealed parts are cached; the render closure is supplied by the caller
// so this file stays free of rendering dependencies. A width change rebuilds
// the cache.
func (p *TextPart) cachedRender(width int, render func(string) string) string {
	if p.done && p.width == width && p.rendered != "" {
		return p.rendered
	}
	p.width = width
	p.rendered = render(p.Text)
	return p.rendered
}

// openThinking returns the trailing open ThinkingPart, creating one when
// needed. New parts start collapsed: thinking shows folded by default.
func openThinking(turn *Turn) *ThinkingPart {
	if n := len(turn.Parts); n > 0 {
		if tp, ok := turn.Parts[n-1].(*ThinkingPart); ok {
			return tp
		}
	}
	tp := &ThinkingPart{collapsed: true}
	turn.Parts = append(turn.Parts, tp)
	return tp
}

// toolPart finds a part by id, or the trailing open one when id is empty.
func toolPart(turn *Turn, id string) *ToolPart {
	if id != "" {
		for _, p := range turn.Parts {
			if tp, ok := p.(*ToolPart); ok && tp.ToolID == id {
				return tp
			}
		}
		return nil
	}
	if n := len(turn.Parts); n > 0 {
		if tp, ok := turn.Parts[n-1].(*ToolPart); ok && !tp.done {
			return tp
		}
	}
	return nil
}

// turnText concatenates the text segments of a turn for history commit.
func turnText(turn *Turn) string {
	var out string
	for _, p := range turn.Parts {
		if tp, ok := p.(*TextPart); ok {
			out += tp.Text
		}
	}
	return out
}

// timeNow and timeSince are indirected so tests can freeze the clock.
var (
	timeNow   = time.Now
	timeSince = time.Since
)
