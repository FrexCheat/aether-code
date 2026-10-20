// Package llm defines the protocol-neutral streaming interface and errors.
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

// InputTokens excludes cache tokens for both providers.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

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
