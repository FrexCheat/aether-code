// Package llm defines the protocol-agnostic abstraction over LLM providers:
// the Client interface with a config-driven factory, the request type, the
// unified stream event types, and classified API errors.
package llm

import (
	"context"
	"net/http"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/tools"
)

// Request is the per-call part of an LLM request: what actually varies from
// one stream to the next. The system prompt and thinking toggle are session-
// wide, so they live on the client and are applied at construction time.
type Request struct {
	Messages []conversation.Message
	Tools    []tools.ToolSchema
}

type Client interface {
	Stream(ctx context.Context, req Request) <-chan StreamEvent
}

// NewClient builds the provider client selected by cfg.Protocol. systemPrompt
// is the session's system prompt, assembled once at startup by the prompt
// package; it is not provider configuration, so it arrives as a parameter
// rather than inside cfg.
func NewClient(cfg config.Provider, httpClient *http.Client, systemPrompt string) (Client, error) {
	switch cfg.Protocol {
	case config.ProtocolAnthropic:
		return newAnthropicClient(cfg, httpClient, systemPrompt), nil
	case config.ProtocolOpenAI:
		return newOpenAIClient(cfg, httpClient, systemPrompt), nil
	default:
		return nil, &InvalidRequestError{
			Message: "unsupported protocol " + string(cfg.Protocol),
		}
	}
}
