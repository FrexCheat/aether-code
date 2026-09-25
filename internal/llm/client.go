// Package llm defines the protocol-agnostic abstraction over LLM providers:
// the Client interface with a config-driven factory, the request type, the
// unified stream event types, and classified API errors.
package llm

import (
	"context"
	"encoding/json"
	"errors"
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
	Stream(ctx context.Context, req Request) <-chan StreamEvent
}

type StreamEvent interface{ streamEvent() }

type TextDelta struct{ Text string }
type TextComplete struct{}

type ThinkingDelta struct{ Text string }
type ThinkingComplete struct {
	Thinking  string
	Signature string
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

type StreamError struct{ Err error }

func (TextDelta) streamEvent()        {}
func (TextComplete) streamEvent()     {}
func (ThinkingDelta) streamEvent()    {}
func (ThinkingComplete) streamEvent() {}
func (ToolCallComplete) streamEvent() {}
func (StreamEnd) streamEvent()        {}
func (StreamError) streamEvent()      {}

// channelBuffer sizes the stream event channels: big enough to absorb a delta
// burst without stalling the SSE reader.
const channelBuffer = 64

// forwardEvent delivers ev to ch, giving up when ctx is cancelled. False
// means the event was not delivered: stop reading and release the connection.
func forwardEvent(ctx context.Context, ch chan<- StreamEvent, ev StreamEvent) bool {
	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// finishEvent delivers the terminal event (StreamEnd or StreamError). The
// two-step select encodes a priority: delivery beats cancellation. A plain
// select would pick randomly when the buffer has room AND ctx is already done,
// silently dropping the terminal event on cancelled streams.
func finishEvent(ctx context.Context, ch chan<- StreamEvent, ev StreamEvent) {
	select {
	case ch <- ev:
		return
	default:
	}
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}

// stream bundles the response channel with the delivery closures both
// adapters send through. The adapter goroutine closes ch on exit; send and
// finish are bound to the request's ctx.
type stream struct {
	ch     chan StreamEvent
	send   func(StreamEvent) bool
	finish func(StreamEvent)
}

func newStream(ctx context.Context) stream {
	ch := make(chan StreamEvent, channelBuffer)
	return stream{
		ch:     ch,
		send:   func(ev StreamEvent) bool { return forwardEvent(ctx, ch, ev) },
		finish: func(ev StreamEvent) { finishEvent(ctx, ch, ev) },
	}
}

// endStream decides the terminal event once an adapter's event loop ends.
// Priority: cancellation, the adapter's already-mapped stream error, a
// failure recorded in-band by the protocol, completion. None of those means
// the stream ended unexpectedly.
func endStream(ctx context.Context, streamErr, failErr error, completed bool, usage Usage) StreamEvent {
	switch {
	case ctx.Err() != nil:
		return StreamError{Err: ctx.Err()}
	case streamErr != nil:
		return StreamError{Err: streamErr}
	case failErr != nil:
		return StreamError{Err: failErr}
	case completed:
		return StreamEnd{Usage: usage}
	default:
		return StreamError{Err: errors.New("stream ended unexpectedly")}
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
