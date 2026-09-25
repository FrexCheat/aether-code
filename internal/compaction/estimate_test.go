package compaction

import (
	"strings"
	"testing"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/tools"
)

func stubSchemas(schemas []tools.ToolSchema) func() []tools.ToolSchema {
	return func() []tools.ToolSchema { return schemas }
}

var testTool = tools.ToolSchema{
	Name:        "mcp__srv__tool",
	Description: "does things",
	InputSchema: map[string]any{"type": "object"},
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"ascii four per token", "abcdefgh", 2},
		{"ascii remainder floors", "abc", 0},
		{"cjk one per rune", "中文中文", 4},
		{"mixed buckets", "ab中", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := estimateTokens(tt.text); got != tt.want {
				t.Errorf("estimateTokens(%q) = %d, want %d", tt.text, got, tt.want)
			}
		})
	}
}

func TestEstimateTokensNoLongerUndercountsCJK(t *testing.T) {
	// The old bytes/3.5 rule scored this at ~85 tokens; the model-facing text
	// of this program is full of such runs.
	text := strings.Repeat("中", 100)
	if got := estimateTokens(text); got != 100 {
		t.Errorf("estimateTokens(CJK x100) = %d, want 100", got)
	}
}

func TestMessageEstimate(t *testing.T) {
	e := NewEstimator("", stubSchemas(nil))
	tests := []struct {
		name string
		msg  conversation.Message
		want int
	}{
		{"text only", conversation.Message{Role: conversation.RoleUser, Content: strings.Repeat("a", 400)}, 104},
		{
			"tool result",
			conversation.Message{ToolResults: []conversation.ToolResultBlock{{ToolUseID: "t", Content: strings.Repeat("x", 400)}}},
			114,
		},
		{
			"tool use",
			conversation.Message{ToolUses: []conversation.ToolUseBlock{{ID: "t", Name: "Read", Args: map[string]any{"path": "a"}}}},
			57,
		},
		{
			"thinking",
			conversation.Message{ThinkingBlocks: []conversation.ThinkingBlock{{Text: strings.Repeat("中", 10)}}},
			14,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := e.Message(tt.msg); got != tt.want {
				t.Errorf("Message() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMessagePricesToolRefs(t *testing.T) {
	e := NewEstimator("", stubSchemas([]tools.ToolSchema{testTool}))
	plain := conversation.Message{ToolResults: []conversation.ToolResultBlock{{ToolUseID: "t", Content: "ok"}}}
	referenced := conversation.Message{ToolResults: []conversation.ToolResultBlock{{
		ToolUseID: "t", Content: "ok", ToolRefs: []string{testTool.Name},
	}}}

	delta := e.Message(referenced) - e.Message(plain)
	if want := e.toolPrice(testTool.Name); delta != want {
		t.Errorf("tool_reference adds %d tokens, want the tool price %d", delta, want)
	}
	if got := e.toolPrice("ghost"); got != unknownToolPrice {
		t.Errorf("unknown tool price = %d, want %d", got, unknownToolPrice)
	}
}

func TestToolPriceHealsAfterSettle(t *testing.T) {
	schemas := []tools.ToolSchema{}
	e := NewEstimator("", func() []tools.ToolSchema { return schemas })
	if got := e.toolPrice("late"); got != unknownToolPrice {
		t.Fatalf("before settle = %d, want %d", got, unknownToolPrice)
	}
	schemas = append(schemas, tools.ToolSchema{Name: "late", Description: "d", InputSchema: map[string]any{}})
	if got := e.toolPrice("late"); got == unknownToolPrice {
		t.Errorf("after settle the registered tool must have its own price")
	}
}

func TestFixedPrefix(t *testing.T) {
	system := "系统提示词"
	e := NewEstimator(system, stubSchemas([]tools.ToolSchema{testTool}))
	want := estimateTokens(system) + e.toolPrice(testTool.Name)
	if got := e.FixedPrefix(); got != want {
		t.Errorf("FixedPrefix() = %d, want %d (system + one tool definition)", got, want)
	}
}

func TestUsedTokensArithmetic(t *testing.T) {
	conv := conversation.NewManager()
	if err := conv.AddUserMessage(strings.Repeat("a", 400)); err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerParams{Conv: conv, Schemas: stubSchemas(nil), SystemPrompt: "系统"})

	full := m.estimator.FixedPrefix() + m.estimator.Messages(conv.Messages())
	if got := m.usedTokens(); got != full {
		t.Errorf("no-anchor estimate = %d, want %d (fixed prefix plus messages)", got, full)
	}

	// The anchor is taken with the first message committed, as the agent does
	// after a round: everything after AnchorCount is estimated incrementally.
	m.RecordUsage(llm.Usage{InputTokens: 1000, OutputTokens: 50})
	if err := conv.AddUserMessage(strings.Repeat("b", 400)); err != nil {
		t.Fatal(err)
	}
	want := 1050 + m.estimator.Messages(conv.Messages()[1:])
	if got := m.usedTokens(); got != want {
		t.Errorf("anchored estimate = %d, want %d", got, want)
	}
}
