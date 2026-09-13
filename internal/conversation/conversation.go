// Package conversation is the provider-agnostic conversation model: the
// unified Message shape consumed by every llm protocol adapter, plus the
// in-memory history manager and the tool-use/tool-result pairing invariant.
//
// Message is a flat record: plain text lives in Content, and each structured
// segment has its own typed field. Protocol-level concerns (which wire role
// carries tool results, merging consecutive turns, wire block construction)
// belong to the llm adapters, not here.
package conversation

// ThinkingBlock carries one thinking segment with its provider identity: the
// Anthropic signature, or the OpenAI reasoning item ID used for replay.
type ThinkingBlock struct {
	Text      string
	Signature string
}

// ToolUseBlock records one tool call issued by the model. Args holds the
// parsed arguments; encoding/json serializes maps with sorted keys, so two
// requests carrying the same args produce identical bytes.
type ToolUseBlock struct {
	ID   string
	Name string
	Args map[string]any
}

// ToolResultBlock is the result of one tool call, paired to its caller by
// ToolUseID.
type ToolResultBlock struct {
	ToolUseID string
	Content   string
	IsError   bool
}

// Role is the author of a conversation message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn in the conversation history. It is the unified
// abstraction that llm adapters translate into their own API shapes.
type Message struct {
	Role           Role
	Content        string
	ThinkingBlocks []ThinkingBlock
	ToolUses       []ToolUseBlock
	ToolResults    []ToolResultBlock
}

// Manager keeps the in-memory conversation history of a single session.
// History is lost on exit. Message order is exactly the call order of the
// Add* methods; protocol-level ordering rules are applied by the llm adapters.
type Manager struct {
	history []Message
}

// NewManager returns an empty conversation.
func NewManager() *Manager {
	return &Manager{}
}

// AddUserMessage appends a user turn to the history.
func (m *Manager) AddUserMessage(content string) {
	m.history = append(m.history, Message{Role: RoleUser, Content: content})
}

// AddAssistantMessage appends a plain-text assistant turn to the history.
func (m *Manager) AddAssistantMessage(content string) {
	m.history = append(m.history, Message{Role: RoleAssistant, Content: content})
}

// AddAssistantFull appends one assistant turn carrying everything the model
// produced: thinking segments, the visible text, and any tool calls.
func (m *Manager) AddAssistantFull(content string, thinking []ThinkingBlock, toolUses []ToolUseBlock) {
	m.history = append(m.history, Message{
		Role:           RoleAssistant,
		Content:        content,
		ThinkingBlocks: thinking,
		ToolUses:       toolUses,
	})
}

// AddToolResults appends a message carrying tool results, paired to their
// callers by ToolUseID. Adapters decide which wire role this maps to.
func (m *Manager) AddToolResults(results ...ToolResultBlock) {
	m.history = append(m.history, Message{Role: RoleUser, ToolResults: results})
}

// GetMessages returns a copy of the history; mutating the result never
// aliases the Manager's internal slice. The per-message block slices are
// copied as well.
func (m *Manager) GetMessages() []Message {
	result := make([]Message, len(m.history))
	for i, msg := range m.history {
		result[i] = msg
		result[i].ThinkingBlocks = copySlice(msg.ThinkingBlocks)
		result[i].ToolUses = copySlice(msg.ToolUses)
		result[i].ToolResults = copySlice(msg.ToolResults)
	}
	return result
}

func copySlice[T any](s []T) []T {
	if s == nil {
		return nil
	}
	out := make([]T, len(s))
	copy(out, s)
	return out
}

// Len returns the number of messages in the history.
func (m *Manager) Len() int {
	return len(m.history)
}

// Clear discards the history.
func (m *Manager) Clear() {
	m.history = nil
}

// TruncateTo keeps the first index messages. Negative indexes are clamped to
// zero; indexes beyond the current length are a no-op.
func (m *Manager) TruncateTo(index int) {
	if index < 0 {
		index = 0
	}
	if index > len(m.history) {
		return
	}
	m.history = m.history[:index]
}
