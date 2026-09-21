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

func (m *Manager) Clear() {
	m.history = nil
}
