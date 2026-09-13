package conversation

import (
	"reflect"
	"testing"
)

func TestEnsureToolPairingLegalSequencePassthrough(t *testing.T) {
	in := []Message{
		{Role: RoleUser, Content: "q"},
		{
			Role:     RoleAssistant,
			ToolUses: []ToolUseBlock{{ID: "t1", Name: "bash"}},
		},
		{
			Role:        RoleUser,
			ToolResults: []ToolResultBlock{{ToolUseID: "t1", Content: "out"}},
		},
	}
	got := EnsureToolPairing(in)
	if !reflect.DeepEqual(got, in) {
		t.Errorf("legal sequence modified: %+v", got)
	}
}

func TestEnsureToolPairingSynthesizesDanglingResults(t *testing.T) {
	in := []Message{
		{Role: RoleUser, Content: "q"},
		{
			Role: RoleAssistant,
			ToolUses: []ToolUseBlock{
				{ID: "t1", Name: "bash"},
				{ID: "t2", Name: "read_file"},
			},
		},
		{
			Role:        RoleUser,
			ToolResults: []ToolResultBlock{{ToolUseID: "t2", Content: "out"}},
		},
	}
	got := EnsureToolPairing(in)
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4: %+v", len(got), got)
	}
	// t1's synthesized result is inserted right after the assistant message.
	inserted := got[2]
	if len(inserted.ToolResults) != 1 {
		t.Fatalf("inserted results = %+v", inserted.ToolResults)
	}
	r := inserted.ToolResults[0]
	if r.ToolUseID != "t1" || !r.IsError || r.Content != InterruptedToolResult {
		t.Errorf("synthesized result = %+v", r)
	}
	// t2's real result stays untouched in its own message.
	if len(got[3].ToolResults) != 1 || got[3].ToolResults[0].ToolUseID != "t2" {
		t.Errorf("real result = %+v", got[3].ToolResults)
	}
}

func TestEnsureToolPairingDropsOrphanResults(t *testing.T) {
	in := []Message{
		{Role: RoleUser, Content: "q"},
		{
			Role:     RoleAssistant,
			ToolUses: []ToolUseBlock{{ID: "t1", Name: "bash"}},
		},
		{
			Role:        RoleUser,
			ToolResults: []ToolResultBlock{{ToolUseID: "t1", Content: "out"}, {ToolUseID: "ghost", Content: "x"}},
		},
		{Role: RoleUser, ToolResults: []ToolResultBlock{{ToolUseID: "ghost2", Content: "y"}}},
	}
	got := EnsureToolPairing(in)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3: %+v", len(got), got)
	}
	if len(got[2].ToolResults) != 1 || got[2].ToolResults[0].ToolUseID != "t1" {
		t.Errorf("kept results = %+v, want only t1", got[2].ToolResults)
	}
}

func TestEnsureToolPairingDropsEmptyShell(t *testing.T) {
	in := []Message{
		{Role: RoleUser, Content: "q"},
		// Message that carried only an orphan result collapses to an empty
		// shell and must be dropped so role alternation survives.
		{Role: RoleUser, ToolResults: []ToolResultBlock{{ToolUseID: "ghost", Content: "x"}}},
		{Role: RoleAssistant, Content: "a"},
	}
	got := EnsureToolPairing(in)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	if got[1].Content != "a" {
		t.Errorf("assistant dropped instead of shell: %+v", got)
	}
}

func TestEnsureToolPairingDoesNotMutateInput(t *testing.T) {
	in := []Message{
		{Role: RoleUser, Content: "q"},
		{
			Role:     RoleAssistant,
			ToolUses: []ToolUseBlock{{ID: "t1", Name: "bash"}},
		},
		{Role: RoleUser, ToolResults: []ToolResultBlock{{ToolUseID: "ghost", Content: "x"}}},
	}
	original := make([]Message, len(in))
	for i, m := range in {
		original[i] = m
		original[i].ToolUses = append([]ToolUseBlock(nil), m.ToolUses...)
		original[i].ToolResults = append([]ToolResultBlock(nil), m.ToolResults...)
	}
	_ = EnsureToolPairing(in)
	if !reflect.DeepEqual(in, original) {
		t.Errorf("input mutated:\n got %+v\nwant %+v", in, original)
	}
}
