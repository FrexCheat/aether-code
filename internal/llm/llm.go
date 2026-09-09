// Package llm defines the protocol-agnostic abstraction over LLM providers:
// unified message and request types, a stream event type, and the Provider
// interface implemented by each backend adapter.
package llm

import "context"

// Role is the author of a conversation message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn in the conversation history.
type Message struct {
	Role    Role
	Content string
}

// Request is a single streaming completion request.
type Request struct {
	// System is the system prompt.
	System string
	// Messages is the full history; the last entry is the current user turn.
	Messages []Message
	// Thinking enables extended thinking / reasoning on providers that support it.
	Thinking bool
}

// StreamEventType discriminates StreamEvent.
type StreamEventType int

const (
	// StreamEventTextDelta carries an increment of assistant text in Text.
	StreamEventTextDelta StreamEventType = iota
	// StreamEventDone marks a normal end of stream. It is the last event before the channel closes.
	StreamEventDone
	// StreamEventError carries a terminal error in Err. It is the last event before the channel closes.
	StreamEventError
)

// StreamEvent is one item on the stream returned by Provider.Stream.
type StreamEvent struct {
	Type StreamEventType
	Text string
	Err  error
}

// Provider is the unified abstraction implemented by every protocol adapter.
//
// Thinking / reasoning increments are consumed and discarded inside the
// adapter; they never appear on the stream.
type Provider interface {
	// Stream starts a streaming request and returns a channel of events.
	//
	// Contract:
	//   - Events are delivered in order.
	//   - The stream ends with exactly one StreamEventDone or StreamEventError,
	//     after which the channel is closed.
	//   - When ctx is cancelled the implementation stops reading, releases the
	//     connection, and ends with StreamEventError carrying ctx.Err().
	//   - Errors that occur before the connection is established are also
	//     reported through the channel, so callers have a single consumption path.
	Stream(ctx context.Context, req Request) <-chan StreamEvent
}
