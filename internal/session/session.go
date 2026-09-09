// Package session keeps the in-memory conversation history and assembles
// requests that carry the full history plus the built-in system prompt.
package session

import (
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/prompt"
)

// Session is a single in-process conversation. History is lost on exit.
type Session struct {
	history []llm.Message
}

// New returns an empty session.
func New() *Session {
	return &Session{}
}

// BuildRequest returns a request carrying the system prompt, the full history
// and userText as the final user turn. It does not modify the history.
func (s *Session) BuildRequest(userText string, thinking bool) llm.Request {
	msgs := make([]llm.Message, 0, len(s.history)+1)
	msgs = append(msgs, s.history...)
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: userText})
	return llm.Request{
		System:   prompt.System,
		Messages: msgs,
		Thinking: thinking,
	}
}

// Commit appends a completed turn (user input and assistant reply) to the history.
// Callers must not call Commit for a failed turn.
func (s *Session) Commit(userText, assistantText string) {
	s.history = append(s.history,
		llm.Message{Role: llm.RoleUser, Content: userText},
		llm.Message{Role: llm.RoleAssistant, Content: assistantText},
	)
}

// Clear discards the history.
func (s *Session) Clear() {
	s.history = nil
}

// Len returns the number of messages in the history.
func (s *Session) Len() int {
	return len(s.history)
}
