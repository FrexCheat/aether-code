package conversation

import "testing"

func TestAddOrder(t *testing.T) {
	m := NewManager()
	m.AddUserMessage("q1")
	m.AddAssistantMessage("a1")
	m.AddUserMessage("q2")
	want := []Message{
		{Role: RoleUser, Content: "q1"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleUser, Content: "q2"},
	}
	got := m.GetMessages()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			t.Errorf("messages[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if m.Len() != len(want) {
		t.Errorf("Len = %d, want %d", m.Len(), len(want))
	}
}

func TestGetMessagesCopy(t *testing.T) {
	m := NewManager()
	m.AddUserMessage("q1")
	got := m.GetMessages()
	got[0].Content = "tampered"
	got = append(got, Message{Role: RoleUser, Content: "extra"})
	if m.Len() != 1 || m.GetMessages()[0].Content != "q1" {
		t.Errorf("history aliased: %+v", m.GetMessages())
	}
}

func TestAddAssistantFullAndToolResults(t *testing.T) {
	m := NewManager()
	m.AddUserMessage("q")
	m.AddAssistantFull("let me check",
		[]ThinkingBlock{{Text: "thinking", Signature: "sig1"}},
		[]ToolUseBlock{{ID: "t1", Name: "read_file", Args: map[string]any{"path": "a.go"}}},
	)
	m.AddToolResults(
		ToolResultBlock{ToolUseID: "t1", Content: "contents"},
		ToolResultBlock{ToolUseID: "t2", Content: "boom", IsError: true},
	)

	got := m.GetMessages()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	a := got[1]
	if a.Role != RoleAssistant || a.Content != "let me check" {
		t.Errorf("assistant = %+v", a)
	}
	if len(a.ThinkingBlocks) != 1 || a.ThinkingBlocks[0].Signature != "sig1" {
		t.Errorf("thinking = %+v", a.ThinkingBlocks)
	}
	if len(a.ToolUses) != 1 || a.ToolUses[0].ID != "t1" || a.ToolUses[0].Name != "read_file" {
		t.Errorf("tool uses = %+v", a.ToolUses)
	}
	r := got[2]
	if r.Role != RoleUser || r.Content != "" || len(r.ToolUses) != 0 {
		t.Errorf("results message = %+v", r)
	}
	if len(r.ToolResults) != 2 || r.ToolResults[0].ToolUseID != "t1" || r.ToolResults[1].IsError != true {
		t.Errorf("tool results = %+v", r.ToolResults)
	}
}

func TestAddAssistantFullNilSlices(t *testing.T) {
	m := NewManager()
	m.AddAssistantFull("reply", nil, nil)
	got := m.GetMessages()
	if got[0].ThinkingBlocks != nil || got[0].ToolUses != nil {
		t.Errorf("nil slices should stay nil: %+v", got[0])
	}
}

func TestBlockSlicesGetMessagesCopy(t *testing.T) {
	m := NewManager()
	m.AddAssistantFull("", nil, []ToolUseBlock{{ID: "t1", Name: "bash"}})
	got := m.GetMessages()
	got[0].ToolUses[0] = ToolUseBlock{ID: "tampered"}
	got[0].ToolUses = append(got[0].ToolUses, ToolUseBlock{ID: "extra"})
	if m.GetMessages()[0].ToolUses[0].ID != "t1" || len(m.GetMessages()[0].ToolUses) != 1 {
		t.Errorf("tool uses aliased: %#v", m.GetMessages()[0].ToolUses)
	}
}

func TestClear(t *testing.T) {
	m := NewManager()
	m.AddUserMessage("q")
	m.AddAssistantMessage("a")
	m.Clear()
	if m.Len() != 0 || len(m.GetMessages()) != 0 {
		t.Errorf("after Clear: Len = %d", m.Len())
	}
	m.AddUserMessage("q2")
	if m.Len() != 1 || m.GetMessages()[0].Content != "q2" {
		t.Errorf("history unusable after Clear: %+v", m.GetMessages())
	}
}
