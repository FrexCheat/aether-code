package session

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FrexCheat/aether-code/internal/conversation"
)

// newTestSession confines the user root and starts one session.
func newTestSession(t *testing.T, conv *conversation.Manager) *Session {
	t.Helper()
	t.Setenv("AETHER_HOME", t.TempDir())
	s, err := New(conv, "/work")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestRoundTrip(t *testing.T) {
	conv := conversation.NewManager()
	s := newTestSession(t, conv)

	if err := conv.AddUserMessage("hello"); err != nil {
		t.Fatal(err)
	}
	if err := conv.AddAssistantMessage("reading", nil, []conversation.ToolUseBlock{
		{ID: "t1", Name: "Read", Args: map[string]any{"file_path": "a.go"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := conv.AddToolResults(conversation.ToolResultBlock{
		ToolUseID: "t1", Content: "package a", ToolRefs: []string{"mcp__srv__tool"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	restored := conversation.NewManager()
	s2, info, err := Resume(restored, s.ID())
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer s2.Close()

	if info.Skipped != 0 || info.Dropped != 0 {
		t.Errorf("info = %+v, want nothing skipped or dropped", info)
	}
	if got, want := restored.Messages(), conv.Messages(); !reflect.DeepEqual(got, want) {
		t.Errorf("restored history differs:\n got %+v\nwant %+v", got, want)
	}
}

func TestBoundaryKeepIsClampedAndRegistered(t *testing.T) {
	conv := conversation.NewManager()
	s := newTestSession(t, conv)
	if err := conv.AddUserMessage("one"); err != nil {
		t.Fatal(err)
	}
	if err := conv.AddUserMessage("two"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// A boundary asking for more messages than the file holds: replay clamps.
	line, err := encodeLine(boundaryRecord{Type: recordBoundary, Content: "SUMMARY", Keep: 99, Ts: time.Now().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	appendFile(t, s.path, line)

	restored := conversation.NewManager()
	s2, _, err := Resume(restored, s.ID())
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer s2.Close()

	msgs := restored.Messages()
	if len(msgs) != 3 || msgs[0].Content != "SUMMARY" {
		t.Fatalf("history = %+v, want the boundary plus both messages", msgs)
	}
	if !s2.IsBoundary("SUMMARY") {
		t.Error("the replayed boundary must be registered as one")
	}
	if s2.IsBoundary("hello") {
		t.Error("a user message must not read as a boundary")
	}
}

func TestUpdateRecordFreezesSpilledContent(t *testing.T) {
	conv := conversation.NewManager()
	s := newTestSession(t, conv)
	if err := conv.AddAssistantMessage("", nil, []conversation.ToolUseBlock{{ID: "t1", Name: "Read"}}); err != nil {
		t.Fatal(err)
	}
	if err := conv.AddToolResults(conversation.ToolResultBlock{ToolUseID: "t1", Content: "the whole file"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	line, err := encodeLine(updateRecord{Type: recordUpdate, ToolUseID: "t1", Content: "preview only", Ts: time.Now().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	appendFile(t, s.path, line)

	restored := conversation.NewManager()
	s2, _, err := Resume(restored, s.ID())
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer s2.Close()
	msgs := restored.Messages()
	if got := msgs[len(msgs)-1].ToolResults[0].Content; got != "preview only" {
		t.Errorf("spilled content = %q, want the frozen replacement", got)
	}
}

func TestReplaySkipsGarbageAndTornLines(t *testing.T) {
	conv := conversation.NewManager()
	s := newTestSession(t, conv)
	if err := conv.AddUserMessage("kept"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	appendFile(t, s.path, []byte("this is not json\n"))
	appendFile(t, s.path, []byte(`{"type":"message","role":"user","content":"torn`)) // no newline

	restored := conversation.NewManager()
	s2, info, err := Resume(restored, s.ID())
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer s2.Close()
	if info.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2", info.Skipped)
	}
	if msgs := restored.Messages(); len(msgs) != 1 || msgs[0].Content != "kept" {
		t.Errorf("history = %+v, want only the intact message", msgs)
	}
}

func TestResumeRefusesNewerVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AETHER_HOME", home)
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "20260101-000000-abcd"
	header, err := encodeLine(headerRecord{Type: recordSession, Version: Version + 1, ID: id, Cwd: "/w", StartedAt: time.Now().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), header, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = Resume(conversation.NewManager(), id)
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Errorf("Resume on a newer version = %v, want a version refusal", err)
	}
}

func TestRemindersAreNeverArchived(t *testing.T) {
	conv := conversation.NewManager()
	s := newTestSession(t, conv)
	if err := conv.AddUserMessage("real words"); err != nil {
		t.Fatal(err)
	}
	if err := conv.AddSystemReminder("session-scoped context"); err != nil {
		t.Fatal(err)
	}
	path := s.path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "session-scoped context") || strings.Contains(string(data), "<system-reminder>") {
		t.Errorf("the archive must not hold reminders:\n%s", data)
	}
	if conv.Len() != 2 {
		t.Errorf("the reminder must still live in the history")
	}
}

func TestFilterUnresolvedTurns(t *testing.T) {
	msgs := []conversation.Message{
		{Role: conversation.RoleAssistant, ToolUses: []conversation.ToolUseBlock{{ID: "t1"}}},
		{Role: conversation.RoleUser, ToolResults: []conversation.ToolResultBlock{{ToolUseID: "t1"}}},
		{Role: conversation.RoleAssistant, ToolUses: []conversation.ToolUseBlock{{ID: "t2"}}},
		{Role: conversation.RoleAssistant, ToolUses: []conversation.ToolUseBlock{{ID: "t3"}, {ID: "t4"}}},
		{Role: conversation.RoleUser, ToolResults: []conversation.ToolResultBlock{{ToolUseID: "t4"}}},
	}
	out, dropped := filterUnresolvedTurns(msgs)
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1 (the t2 turn)", dropped)
	}
	if len(out) != 4 {
		t.Fatalf("kept %d messages, want 4", len(out))
	}
	if got := out[2].ToolUses; len(got) != 2 || got[0].ID != "t3" || got[1].ID != "t4" {
		t.Errorf("a partly resolved turn must stay whole: %+v", got)
	}
}

func TestLockSemantics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("liveness probing differs on windows")
	}
	path := filepath.Join(t.TempDir(), "s.lock")

	if err := acquireLock(path); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := acquireLock(path); err == nil {
		t.Error("a lock held by a live process must not be stolen")
	} else if !strings.Contains(err.Error(), "in use") {
		t.Errorf("second acquire = %v, want an in-use refusal", err)
	}

	releaseLock(path)
	if err := acquireLock(path); err != nil {
		t.Errorf("acquire after release: %v", err)
	}
	releaseLock(path)

	// A lock naming a dead process is a crash's leftover: takeover succeeds
	// without manual cleanup.
	if err := os.WriteFile(path, []byte(strconv.Itoa(999999)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := acquireLock(path); err != nil {
		t.Errorf("dead-pid takeover: %v", err)
	}
	releaseLock(path)
}

func appendFile(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
