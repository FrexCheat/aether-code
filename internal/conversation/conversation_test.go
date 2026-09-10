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
		if got[i] != want[i] {
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

func TestTruncateTo(t *testing.T) {
	m := NewManager()
	m.AddUserMessage("q1")
	m.AddAssistantMessage("a1")
	m.AddUserMessage("q2")

	m.TruncateTo(2)
	if m.Len() != 2 || m.GetMessages()[1].Role != RoleAssistant {
		t.Errorf("TruncateTo(2): %+v", m.GetMessages())
	}

	m.TruncateTo(99) // beyond length: no-op
	if m.Len() != 2 {
		t.Errorf("TruncateTo(99) changed history: Len = %d", m.Len())
	}

	m.TruncateTo(-1)
	if m.Len() != 0 {
		t.Errorf("TruncateTo(-1): Len = %d", m.Len())
	}
}
