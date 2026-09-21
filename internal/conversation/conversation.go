package conversation

import (
	"slices"
	"strings"
)

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
	ToolRefs  []string
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

func (m *Manager) AddAssistantMessage(content string, thinking []ThinkingBlock, toolUses []ToolUseBlock) {
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
	m.history = append(m.history, NewReminderMessage(content))
}

func NewReminderMessage(content string) Message {
	content = strings.TrimSpace(content)
	return Message{
		Role: RoleUser,
		Content: "<" + reminderTag + ">\n" + content +
			"\n\n" + reminderNotice + "\n</" + reminderTag + ">",
	}
}

func (m *Manager) Messages() []Message {
	result := make([]Message, len(m.history))
	for i, msg := range m.history {
		result[i] = msg
		result[i].ThinkingBlocks = slices.Clone(msg.ThinkingBlocks)
		result[i].ToolUses = slices.Clone(msg.ToolUses)
		result[i].ToolResults = slices.Clone(msg.ToolResults)
		for j, tr := range result[i].ToolResults {
			result[i].ToolResults[j].ToolRefs = slices.Clone(tr.ToolRefs)
		}
	}
	return result
}

func (m *Manager) Len() int {
	return len(m.history)
}

// ReplaceToolResult rewrites one tool result in place, matched by tool-use ID.
func (m *Manager) ReplaceToolResult(toolUseID, content string) bool {
	for i := range m.history {
		for j := range m.history[i].ToolResults {
			if m.history[i].ToolResults[j].ToolUseID == toolUseID {
				m.history[i].ToolResults[j].Content = content
				return true
			}
		}
	}
	return false
}

// ReplacePrefix swaps history[:keepFrom] for summary and keeps the tail
// verbatim; out-of-range keepFrom is a no-op so a bad boundary cannot corrupt
// the history.
func (m *Manager) ReplacePrefix(summary Message, keepFrom int) {
	if keepFrom <= 0 || keepFrom > len(m.history) {
		return
	}
	m.history = append([]Message{summary}, m.history[keepFrom:]...)
}

func (m *Manager) Clear() {
	m.history = nil
}
