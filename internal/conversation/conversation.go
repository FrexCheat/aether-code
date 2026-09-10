// Package conversation is the provider-agnostic conversation manager: it owns
// the unified Message abstraction consumed by every llm protocol adapter and
// keeps the in-memory history ordered. Request assembly (system prompt,
// thinking, protocol-specific merging) belongs to the callers, not here.
package conversation

// Role is the author of a conversation message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn in the conversation history. It is the unified
// abstraction that llm adapters translate into their own API shapes.
//
// Future message forms (thinking blocks, tool use / tool results) extend this
// struct in place; the reference design reserves room for them here.
type Message struct {
	Role    Role
	Content string
}

// Manager keeps the in-memory conversation history of a single session.
// History is lost on exit. Message order is exactly the call order of the
// Add* methods; protocol-level ordering rules (e.g. Anthropic merging
// consecutive same-role turns) are applied by the llm adapters.
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

// AddAssistantMessage appends an assistant turn to the history.
func (m *Manager) AddAssistantMessage(content string) {
	m.history = append(m.history, Message{Role: RoleAssistant, Content: content})
}

// GetMessages returns a copy of the history; mutating the result never
// aliases the Manager's internal slice.
func (m *Manager) GetMessages() []Message {
	result := make([]Message, len(m.history))
	copy(result, m.history)
	return result
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
