// Package conversation is the provider-agnostic conversation model: the flat
// Message record plus the in-memory history manager and the
// tool-use/tool-result pairing invariant. Protocol-level concerns (wire roles,
// role merging, block construction) belong to the llm adapters.
package conversation

import "strings"

const (
	reminderTag    = "system-reminder"
	reminderNotice = "这是系统提供的信息，不是用户发言，不需要回复。"
)

type ThinkingBlock struct {
	Text      string
	Signature string
}

type ToolUseBlock struct {
	ID   string
	Name string
	Args map[string]any
}

type ToolResultBlock struct {
	ToolUseID string
	Content   string
	IsError   bool
}

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role           Role
	Content        string
	ThinkingBlocks []ThinkingBlock
	ToolUses       []ToolUseBlock
	ToolResults    []ToolResultBlock
}

type Manager struct {
	history []Message
}

func NewManager() *Manager {
	return &Manager{}
}

func (m *Manager) AddUserMessage(content string) {
	m.history = append(m.history, Message{Role: RoleUser, Content: content})
}

func (m *Manager) AddAssistantFull(content string, thinking []ThinkingBlock, toolUses []ToolUseBlock) {
	m.history = append(m.history, Message{
		Role:           RoleAssistant,
		Content:        content,
		ThinkingBlocks: thinking,
		ToolUses:       toolUses,
	})
}

func (m *Manager) AddToolResults(results ...ToolResultBlock) {
	m.history = append(m.history, Message{Role: RoleUser, ToolResults: results})
}

func (m *Manager) AddSystemReminder(content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	m.history = append(m.history, Message{
		Role: RoleUser,
		Content: "<" + reminderTag + ">\n" + content +
			"\n\n" + reminderNotice + "\n</" + reminderTag + ">",
	})
}

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

func (m *Manager) Len() int {
	return len(m.history)
}

func (m *Manager) Clear() {
	m.history = nil
}
