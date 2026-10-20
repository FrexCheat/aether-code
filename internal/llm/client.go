// Package llm defines the protocol-agnostic abstraction over LLM providers:
// the Client interface with a config-driven factory, the request type, the
// unified stream event types, and classified API errors.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"strings"

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
	Stream(ctx context.Context, req Request) iter.Seq2[StreamEvent, error]
}

type StreamEvent interface{ streamEvent() }

type TextDelta struct{ Text string }
type TextComplete struct{}

type ThinkingKind uint8

const (
	ThinkingUnknown ThinkingKind = iota
	ThinkingSummary
	ThinkingContent
)

type ThinkingDelta struct {
	Kind ThinkingKind
	Text string
}

// ThinkingComplete is one finished reasoning block; the fields carry whatever
// the provider needs to see again on the next request (see
// conversation.ThinkingBlock for the per-protocol split).
type ThinkingComplete struct {
	Thinking  string
	Content   string
	Signature string
	Encrypted string
}

type ToolCallComplete struct {
	ToolID    string
	ToolName  string
	Arguments map[string]any
}

// Usage is one request's token accounting, normalized across providers:
// InputTokens excludes cache tokens — Anthropic reports them separately, and
// the OpenAI adapter subtracts its inclusive cached/cache-write counts — so
// the prompt size is the three input sides summed, never a bare field read.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

// PromptTokens is the prompt size the request carried; TotalTokens adds the
// output, which the next request replays as input.
func (u Usage) PromptTokens() int {
	return u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
}

func (u Usage) TotalTokens() int { return u.PromptTokens() + u.OutputTokens }

type StreamEnd struct {
	Usage Usage
}

func (TextDelta) streamEvent()        {}
func (TextComplete) streamEvent()     {}
func (ThinkingDelta) streamEvent()    {}
func (ThinkingComplete) streamEvent() {}
func (ToolCallComplete) streamEvent() {}
func (StreamEnd) streamEvent()        {}

// streamResult decides how an adapter's event loop ended. Cancellation wins
// over protocol errors so a cancelled turn does not surface a provider error.
func streamResult(ctx context.Context, streamErr, failErr error, completed bool) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case streamErr != nil:
		return streamErr
	case failErr != nil:
		return failErr
	case completed:
		return nil
	default:
		return errors.New("stream ended unexpectedly")
	}
}

// parseToolArgs decodes an accumulated tool-use JSON string into a map;
// malformed (e.g. truncated) input yields an empty map rather than failing the
// stream.
func parseToolArgs(raw string) map[string]any {
	args := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return args
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return map[string]any{}
	}
	return args
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
