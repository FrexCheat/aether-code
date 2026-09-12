// Package conversation is the provider-agnostic conversation manager: it owns
// the unified Message abstraction consumed by every llm protocol adapter and
// keeps the in-memory history ordered. Request assembly (system prompt,
// thinking, protocol-specific merging) belongs to the callers, not here.
package conversation

// Block is one content fragment of a message. Sealed: only the concrete
// types below are valid, adapters switch on them when translating to wire
// format.
type Block interface{ block() }

// TextBlock is a plain text fragment of an assistant message.
type TextBlock struct{ Text string }

// ThinkingBlock carries one thinking segment with its provider signature.
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
// ToolUseID. Anthropic semantics put these in a user-role message.
type ToolResultBlock struct {
	ToolUseID string
	Content   string
	IsError   bool
}

func (TextBlock) block()       {}
func (ThinkingBlock) block()   {}
func (ToolUseBlock) block()    {}
func (ToolResultBlock) block() {}

// Role is the author of a conversation message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn in the conversation history. It is the unified
// abstraction that llm adapters translate into their own API shapes.
//
// Content is the plain-text form: for messages without blocks it is the whole
// content, and adapters keep using it directly. When Blocks is non-empty it
// is authoritative and Content is ignored.
type Message struct {
	Role    Role
	Content string
	Blocks  []Block
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

// AddAssistantBlocks appends an assistant turn carrying content blocks
// (text, thinking, tool use). Used when the model calls tools mid-reply.
func (m *Manager) AddAssistantBlocks(blocks ...Block) {
	m.history = append(m.history, Message{Role: RoleAssistant, Blocks: blocks})
}

// AddToolResults appends a user-role message carrying tool results, matching
// Anthropic semantics where tool results ride in the next user turn.
func (m *Manager) AddToolResults(results ...ToolResultBlock) {
	blocks := make([]Block, len(results))
	for i, r := range results {
		blocks[i] = r
	}
	m.history = append(m.history, Message{Role: RoleUser, Blocks: blocks})
}

// GetMessages returns a copy of the history; mutating the result never
// aliases the Manager's internal slice. Block slices are copied as well.
func (m *Manager) GetMessages() []Message {
	result := make([]Message, len(m.history))
	for i, msg := range m.history {
		result[i] = msg
		if msg.Blocks != nil {
			result[i].Blocks = make([]Block, len(msg.Blocks))
			copy(result[i].Blocks, msg.Blocks)
		}
	}
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
