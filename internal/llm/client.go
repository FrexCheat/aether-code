// Package llm defines the protocol-agnostic abstraction over LLM providers:
// a unified Client interface with a config-driven factory, the request type
// carrying conversation messages, stream events, and classified API errors.
//
// Layout (flat, no subpackages):
//
//	client.go    — Client interface, Request, NewClient factory
//	events.go    — StreamEvent / StreamEventType
//	errors.go    — five classified API error types + classify
//	anthropic.go — Anthropic Messages API adapter
//	openai.go    — OpenAI Responses API adapter
package llm

import (
	"context"
	"net/http"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/tools"
)

// Request is a single streaming completion request.
type Request struct {
	// System is the system prompt.
	System string
	// Messages is the full history; the last entry is the current user turn.
	Messages []conversation.Message
	// Thinking enables extended thinking / reasoning on providers that support it.
	Thinking bool
	// Tools is the tool list in the provider-neutral shape produced by
	// tools.Registry. Adapters translate it into their own API format;
	// empty means no tools are offered this request.
	Tools []tools.ToolSchema
}

// Client is the unified abstraction implemented by every protocol adapter.
//
// Events are typed: consumers switch on the concrete StreamEvent variant
// (TextDelta, ThinkingDelta, ThinkingComplete, ToolCall*, StreamEnd,
// StreamError).
type Client interface {
	// Stream starts a streaming request and returns a channel of events.
	//
	// Contract:
	//   - Events are delivered in order.
	//   - The stream ends with exactly one StreamEnd or StreamError,
	//     after which the channel is closed.
	//   - When ctx is cancelled the implementation stops reading, releases the
	//     connection, and ends with StreamError carrying ctx.Err().
	//   - Errors that occur before the connection is established are also
	//     reported through the channel, so callers have a single consumption path.
	Stream(ctx context.Context, req Request) <-chan StreamEvent
}

// NewClient returns the adapter matching cfg.Protocol. httpClient may be nil
// to use the SDK default transport; tests pass one pointing at an
// httptest.Server. An unknown protocol yields an InvalidRequestError.
func NewClient(cfg config.Provider, httpClient *http.Client) (Client, error) {
	switch cfg.Protocol {
	case config.ProtocolAnthropic:
		return newAnthropicClient(cfg, httpClient), nil
	case config.ProtocolOpenAI:
		return newOpenAIClient(cfg, httpClient), nil
	default:
		return nil, &InvalidRequestError{
			Message: "unsupported protocol " + string(cfg.Protocol),
		}
	}
}
