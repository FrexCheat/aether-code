package conversation

import (
	"errors"
	"testing"
)

// refusingJournal fails every write, to prove the write-ahead discipline:
// a refused journal write must leave the history untouched.
type refusingJournal struct {
	err    error
	writes int
}

func (j *refusingJournal) RecordAppend(Message) error { j.writes++; return j.err }
func (j *refusingJournal) RecordReplacePrefix(Message, int) error {
	j.writes++
	return j.err
}
func (j *refusingJournal) RecordReplaceToolResult(string, string) error { j.writes++; return j.err }

func TestJournalWriteAhead(t *testing.T) {
	cause := errors.New("archive refused")
	j := &refusingJournal{err: cause}
	m := NewManager()
	m.SetJournal(j)

	if err := m.AddUserMessage("hello"); !errors.Is(err, cause) {
		t.Fatalf("AddUserMessage = %v, want the journal error", err)
	}
	if m.Len() != 0 {
		t.Errorf("a refused write must not land in history: len = %d", m.Len())
	}

	// ReplacePrefix consults the journal before touching the history.
	m.SetJournal(nil)
	if err := m.AddUserMessage("one"); err != nil {
		t.Fatal(err)
	}
	if err := m.AddUserMessage("two"); err != nil {
		t.Fatal(err)
	}
	m.SetJournal(j)
	if err := m.ReplacePrefix(Message{Role: RoleUser, Content: "summary"}, 1); !errors.Is(err, cause) {
		t.Fatalf("ReplacePrefix = %v, want the journal error", err)
	}
	if m.Len() != 2 || m.Messages()[0].Content != "one" {
		t.Errorf("a refused rewrite must leave the history untouched")
	}
	m.SetJournal(nil)
	if err := m.ReplacePrefix(Message{Role: RoleUser, Content: "summary"}, 1); err != nil {
		t.Fatal(err)
	}
	if msgs := m.Messages(); len(msgs) != 2 || msgs[0].Content != "summary" || msgs[1].Content != "two" {
		t.Errorf("ReplacePrefix = %+v, want summary plus the kept tail", msgs)
	}
}

func TestReplacePrefixOutOfRangeIsNoop(t *testing.T) {
	m := NewManager()
	if err := m.AddUserMessage("one"); err != nil {
		t.Fatal(err)
	}
	j := &refusingJournal{err: errors.New("must not be consulted")}
	m.SetJournal(j)
	for _, keep := range []int{0, -1, 5} {
		if err := m.ReplacePrefix(Message{Role: RoleUser, Content: "x"}, keep); err != nil {
			t.Errorf("keep=%d = %v, want a silent no-op", keep, err)
		}
	}
	if j.writes != 0 || m.Len() != 1 {
		t.Errorf("out-of-range rewrites must not reach the journal or the history")
	}
}

func TestReplaceToolResult(t *testing.T) {
	m := NewManager()
	if err := m.AddToolResults(ToolResultBlock{ToolUseID: "t1", Content: "original"}); err != nil {
		t.Fatal(err)
	}
	found, err := m.ReplaceToolResult("t1", "replacement")
	if !found || err != nil {
		t.Fatalf("ReplaceToolResult = %v, %v; want found", found, err)
	}
	if got := m.Messages()[0].ToolResults[0].Content; got != "replacement" {
		t.Errorf("content = %q", got)
	}
	found, err = m.ReplaceToolResult("missing", "x")
	if found || err != nil {
		t.Errorf("missing id = %v, %v; want (false, nil)", found, err)
	}
}

func TestAddToolResultsEmptyIsNoop(t *testing.T) {
	j := &refusingJournal{err: errors.New("must not be consulted")}
	m := NewManager()
	m.SetJournal(j)
	if err := m.AddToolResults(); err != nil {
		t.Errorf("empty AddToolResults = %v, want nil", err)
	}
	if j.writes != 0 || m.Len() != 0 {
		t.Errorf("an empty results batch must be a complete no-op")
	}
}

func TestEnsureReminder(t *testing.T) {
	m := NewManager()
	added, err := EnsureReminder(m, "marker line", "marker line\nbody")
	if !added || err != nil {
		t.Fatalf("first EnsureReminder = %v, %v", added, err)
	}
	if added, _ := EnsureReminder(m, "marker line", "marker line\nbody"); added {
		t.Error("the marker is already present; nothing may be added")
	}
	if added, _ := EnsureReminder(m, "other", ""); added {
		t.Error("empty text must not inject")
	}
	msg := m.Messages()[0]
	if !IsReminder(msg) {
		t.Error("the injected message must read as a reminder")
	}
	if m.ContentCount(0) != 0 {
		t.Errorf("ContentCount = %d, want 0 (only a reminder is stored)", m.ContentCount(0))
	}
}

func TestEnsureToolPairing(t *testing.T) {
	// Dangling call: an error result is synthesized right after it.
	msgs := []Message{
		{Role: RoleAssistant, Content: "reading", ToolUses: []ToolUseBlock{{ID: "t1", Name: "Read"}}},
	}
	fixed := EnsureToolPairing(msgs)
	if len(fixed) != 2 {
		t.Fatalf("fixed = %+v, want the call plus a synthesized result", fixed)
	}
	if tr := fixed[1].ToolResults; len(tr) != 1 || tr[0].ToolUseID != "t1" || !tr[0].IsError {
		t.Errorf("synthesized result = %+v", tr)
	}

	// Orphan result: dropped, and an emptied shell disappears entirely.
	orphan := []Message{
		{Role: RoleUser, ToolResults: []ToolResultBlock{{ToolUseID: "gone", Content: "x"}}},
		{Role: RoleUser, Content: "still here"},
	}
	fixed = EnsureToolPairing(orphan)
	if len(fixed) != 1 || fixed[0].Content != "still here" {
		t.Errorf("fixed = %+v, want the orphan dropped", fixed)
	}

	// A healthy exchange passes through untouched.
	healthy := []Message{
		{Role: RoleAssistant, ToolUses: []ToolUseBlock{{ID: "t1"}}},
		{Role: RoleUser, ToolResults: []ToolResultBlock{{ToolUseID: "t1"}}},
	}
	if got := EnsureToolPairing(healthy); len(got) != 2 {
		t.Errorf("healthy history was rewritten: %+v", got)
	}
}

func TestMessagesIsADeepClone(t *testing.T) {
	m := NewManager()
	if err := m.AddToolResults(ToolResultBlock{ToolUseID: "t1", Content: "x", ToolRefs: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	out := m.Messages()
	out[0].ToolResults[0].Content = "mutated"
	out[0].ToolResults[0].ToolRefs[0] = "mutated"
	fresh := m.Messages()
	if fresh[0].ToolResults[0].Content != "x" || fresh[0].ToolResults[0].ToolRefs[0] != "a" {
		t.Errorf("Messages() must deep-clone: %+v", fresh)
	}
}
