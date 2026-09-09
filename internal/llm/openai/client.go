// Package openai adapts the OpenAI Responses API (via the official Go SDK)
// to the llm.Provider interface.
package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/llm"
)

const channelBuffer = 64

// Client is an llm.Provider backed by the OpenAI Responses API.
type Client struct {
	sdk   openai.Client
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
	return &Client{sdk: openai.NewClient(opts...), model: cfg.Model}
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

		stream := c.sdk.Responses.NewStreaming(ctx, c.buildParams(req))
		defer stream.Close()

		sawCompleted := false
		var failErr error
		for stream.Next() {
			ev := stream.Current()
			switch ev.Type {
			case "response.output_text.delta":
				if !send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Text: ev.Delta}) {
					return
				}
			case "response.completed":
				sawCompleted = true
			case "response.failed", "response.incomplete":
				detail := ev.Response.Error.Message
				if ev.Response.Error.Code != "" {
					detail = fmt.Sprintf("%s: %s", ev.Response.Error.Code, detail)
				}
				if detail == "" && ev.Response.IncompleteDetails.Reason != "" {
					detail = string(ev.Response.IncompleteDetails.Reason)
				}
				if detail == "" {
					detail = ev.Type
				}
				failErr = fmt.Errorf("%s: %s", ev.Type, detail)
			case "error":
				failErr = fmt.Errorf("%s: %s", ev.Code, ev.Message)
			default:
				// reasoning.*, output_item.*, content_part.*, *.done etc. are ignored.
			}
		}

		switch {
		case ctx.Err() != nil:
			finish(llm.StreamEvent{Type: llm.StreamEventError, Err: ctx.Err()})
		case stream.Err() != nil:
			finish(llm.StreamEvent{Type: llm.StreamEventError, Err: stream.Err()})
		case failErr != nil:
			finish(llm.StreamEvent{Type: llm.StreamEventError, Err: failErr})
		case sawCompleted:
			finish(llm.StreamEvent{Type: llm.StreamEventDone})
		default:
			finish(llm.StreamEvent{Type: llm.StreamEventError, Err: errors.New("stream ended unexpectedly")})
		}
	}()
	return ch
}

func (c *Client) buildParams(req llm.Request) responses.ResponseNewParams {
	items := make(responses.ResponseInputParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := responses.EasyInputMessageRoleUser
		if m.Role == llm.RoleAssistant {
			role = responses.EasyInputMessageRoleAssistant
		}
		items = append(items, responses.ResponseInputItemParamOfMessage(m.Content, role))
	}
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(c.model),
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: items},
	}
	if req.System != "" {
		params.Instructions = openai.String(req.System)
	}
	if req.Thinking {
		params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffortMedium}
	}
	return params
}
