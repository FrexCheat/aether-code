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

// Journal receives every history mutation before it lands: a non-nil error
// refuses the mutation, so the on-disk record always exists before the
// in-memory history does. Callbacks run on the caller's goroutine and must
// not call back into the Manager.
type Journal interface {
	RecordAppend(msg Message) error
	RecordReplacePrefix(summary Message, keepFrom int) error
	RecordReplaceToolResult(toolUseID, content string) error
}

type Manager struct {
	history []Message
	journal Journal
}

func NewManager() *Manager {
	return &Manager{}
}

// SetJournal attaches the write-ahead journal; nil detaches.
func (m *Manager) SetJournal(j Journal) {
	m.journal = j
}

func (m *Manager) AddUserMessage(content string) error {
	return m.appendMessage(Message{Role: RoleUser, Content: content})
}

func (m *Manager) AddAssistantMessage(content string, thinking []ThinkingBlock, toolUses []ToolUseBlock) error {
	return m.appendMessage(Message{
		Role:           RoleAssistant,
		Content:        content,
		ThinkingBlocks: thinking,
		ToolUses:       toolUses,
	})
}

func (m *Manager) AddToolResults(results ...ToolResultBlock) error {
	if len(results) == 0 {
		return nil
	}
	return m.appendMessage(Message{Role: RoleUser, ToolResults: results})
}

func (m *Manager) AddSystemReminder(content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	return m.appendMessage(NewReminderMessage(content))
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
		result[i] = cloneMessage(msg)
	}
	return result
}

func (m *Manager) Len() int {
	return len(m.history)
}

// ReplaceToolResult rewrites one tool result in place, matched by tool-use
// ID; not found is (false, nil) — a missing entry is a state question, not a
// write failure.
func (m *Manager) ReplaceToolResult(toolUseID, content string) (bool, error) {
	for i := range m.history {
		for j := range m.history[i].ToolResults {
			if m.history[i].ToolResults[j].ToolUseID != toolUseID {
				continue
			}
			if m.journal != nil {
				if err := m.journal.RecordReplaceToolResult(toolUseID, content); err != nil {
					return false, err
				}
			}
			m.history[i].ToolResults[j].Content = content
			return true, nil
		}
	}
	return false, nil
}

// ReplacePrefix swaps history[:keepFrom] for summary and keeps the tail
// verbatim; out-of-range keepFrom is a no-op so a bad boundary cannot corrupt
// the history.
func (m *Manager) ReplacePrefix(summary Message, keepFrom int) error {
	if keepFrom <= 0 || keepFrom > len(m.history) {
		return nil
	}
	if m.journal != nil {
		if err := m.journal.RecordReplacePrefix(summary, keepFrom); err != nil {
			return err
		}
	}
	m.history = append([]Message{summary}, m.history[keepFrom:]...)
	return nil
}

// Load replaces the history wholesale without notifying the journal: it is the
// session-restore path and runs before the journal is attached. The slice is
// cloned — the caller keeps its own copy.
func (m *Manager) Load(messages []Message) {
	m.history = make([]Message, len(messages))
	for i, msg := range messages {
		m.history[i] = cloneMessage(msg)
	}
}

// Clear empties the history and is never journaled: /clear always rotates the
// session file instead, and the old file stays untouched.
func (m *Manager) Clear() {
	m.history = nil
}

// ContentCount reports how many messages in history[from:] are conversation
// content rather than injected reminders; the session computes a boundary's
// keep with it, since reminders never reach the archive.
func (m *Manager) ContentCount(from int) int {
	if from < 0 {
		from = 0
	}
	if from > len(m.history) {
		return 0
	}
	n := 0
	for _, msg := range m.history[from:] {
		if !IsReminder(msg) {
			n++
		}
	}
	return n
}

// IsReminder reports whether the message is one of the injected
// <system-reminder> wrappers rather than conversation content.
func IsReminder(msg Message) bool {
	return strings.HasPrefix(msg.Content, "<"+reminderTag+">")
}

// EnsureReminder appends the reminder unless the marker already appears in the
// history or text is empty; reports whether it added one. The marker is
// expected to be a stable first line of text (see agent.deferredMarker).
func EnsureReminder(m *Manager, marker, text string) (bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return false, nil
	}
	if marker != "" {
		for i := range m.history {
			if strings.Contains(m.history[i].Content, marker) {
				return false, nil
			}
		}
	}
	if err := m.appendMessage(NewReminderMessage(text)); err != nil {
		return false, err
	}
	return true, nil
}

// appendMessage journals first, then lands the message — the write-ahead
// order the session archive relies on.
func (m *Manager) appendMessage(msg Message) error {
	if m.journal != nil {
		if err := m.journal.RecordAppend(msg); err != nil {
			return err
		}
	}
	m.history = append(m.history, msg)
	return nil
}

func cloneMessage(msg Message) Message {
	out := msg
	out.ThinkingBlocks = slices.Clone(msg.ThinkingBlocks)
	out.ToolUses = slices.Clone(msg.ToolUses)
	out.ToolResults = slices.Clone(msg.ToolResults)
	for j, tr := range out.ToolResults {
		out.ToolResults[j].ToolRefs = slices.Clone(tr.ToolRefs)
	}
	return out
}
