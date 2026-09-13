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

type Request struct {
	System   string
	Messages []conversation.Message
	Thinking bool
	Tools    []tools.ToolSchema
}

type Client interface {
	Stream(ctx context.Context, req Request) <-chan StreamEvent
}

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
