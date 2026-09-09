package session

import (
	"testing"

	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/prompt"
)

func TestBuildRequestEmpty(t *testing.T) {
	s := New()
	req := s.BuildRequest("hi", true)
	if len(req.Messages) != 1 {
		t.Fatalf("Messages len = %d, want 1", len(req.Messages))
	}
	if req.Messages[0].Role != llm.RoleUser || req.Messages[0].Content != "hi" {
		t.Errorf("unexpected message: %+v", req.Messages[0])
	}
	if req.System != prompt.System {
		t.Errorf("System = %q", req.System)
	}
	if !req.Thinking {
		t.Error("Thinking should be true")
	}
	if s.Len() != 0 {
		t.Errorf("BuildRequest must not modify history, Len = %d", s.Len())
	}
}

func TestCommitThenBuild(t *testing.T) {
	s := New()
	s.Commit("q1", "a1")
	if s.Len() != 2 {
		t.Fatalf("Len = %d, want 2", s.Len())
	}
	req := s.BuildRequest("q2", false)
	if len(req.Messages) != 3 {
		t.Fatalf("Messages len = %d, want 3", len(req.Messages))
	}
	want := []llm.Message{
		{Role: llm.RoleUser, Content: "q1"},
		{Role: llm.RoleAssistant, Content: "a1"},
		{Role: llm.RoleUser, Content: "q2"},
	}
	for i, m := range want {
		if req.Messages[i] != m {
			t.Errorf("Messages[%d] = %+v, want %+v", i, req.Messages[i], m)
		}
	}
}

func TestClear(t *testing.T) {
	s := New()
	s.Commit("q", "a")
	s.Clear()
	if s.Len() != 0 {
		t.Errorf("Len after Clear = %d", s.Len())
	}
}

func TestBuildRequestDoesNotAlias(t *testing.T) {
	s := New()
	s.Commit("q1", "a1")
	req := s.BuildRequest("q2", false)
	req.Messages[0].Content = "tampered"
	s.Commit("q2", "a2")
	again := s.BuildRequest("q3", false)
	if again.Messages[0].Content != "q1" {
		t.Errorf("history aliased: %q", again.Messages[0].Content)
	}
}
