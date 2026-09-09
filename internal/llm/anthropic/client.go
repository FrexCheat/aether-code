// Package anthropic adapts the Anthropic Messages API (via the official Go SDK)
// to the llm.Provider interface.
package anthropic

import (
	"context"
	"errors"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/llm"
)

const (
	maxTokens      = 16384
	thinkingBudget = 8192
	channelBuffer  = 64
)

// Client is an llm.Provider backed by the Anthropic Messages API.
type Client struct {
	sdk   anthropic.Client
	model string
}

var _ llm.Provider = (*Client)(nil)

// New builds a client from a provider config. httpClient may be nil to use the
// SDK default; tests pass one pointing at an httptest.Server.
func New(cfg config.Provider, httpClient *http.Client) *Client {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithMaxRetries(0),
	}
	if httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	return &Client{sdk: anthropic.NewClient(opts...), model: cfg.Model}
}

// Stream implements llm.Provider.
func (c *Client) Stream(ctx context.Context, req llm.Request) <-chan llm.StreamEvent {
	ch := make(chan llm.StreamEvent, channelBuffer)
	go func() {
		defer close(ch)
		send := func(ev llm.StreamEvent) bool {
			select {
			case ch <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}
		// finish delivers the terminal event. It tries a non-blocking send first
		// so that a cancelled ctx never races the event out of the channel; it
		// only blocks (cancellably) when the buffer is full.
		finish := func(ev llm.StreamEvent) {
			select {
			case ch <- ev:
			default:
				select {
				case ch <- ev:
				case <-ctx.Done():
				}
			}
		}

		params := c.buildParams(req)
		stream := c.sdk.Messages.NewStreaming(ctx, params)
		defer stream.Close()

		sawStop := false
		for stream.Next() {
			switch ev := stream.Current().AsAny().(type) {
			case anthropic.ContentBlockDeltaEvent:
				switch d := ev.Delta.AsAny().(type) {
				case anthropic.TextDelta:
					if !send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Text: d.Text}) {
						return
					}
				default:
					// ThinkingDelta, SignatureDelta and others are dropped.
				}
			case anthropic.MessageStopEvent:
				sawStop = true
			}
		}

		switch {
		case ctx.Err() != nil:
			finish(llm.StreamEvent{Type: llm.StreamEventError, Err: ctx.Err()})
		case stream.Err() != nil:
			finish(llm.StreamEvent{Type: llm.StreamEventError, Err: stream.Err()})
		case sawStop:
			finish(llm.StreamEvent{Type: llm.StreamEventDone})
		default:
			finish(llm.StreamEvent{Type: llm.StreamEventError, Err: errors.New("stream ended unexpectedly")})
		}
	}()
	return ch
}

func (c *Client) buildParams(req llm.Request) anthropic.MessageNewParams {
	msgs := make([]anthropic.MessageParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		block := anthropic.NewTextBlock(m.Content)
		if m.Role == llm.RoleAssistant {
			msgs = append(msgs, anthropic.NewAssistantMessage(block))
		} else {
			msgs = append(msgs, anthropic.NewUserMessage(block))
		}
	}
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: maxTokens,
		Messages:  msgs,
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	if req.Thinking {
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(thinkingBudget)
	}
	return params
}
