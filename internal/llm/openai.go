// openai.go — OpenAI Responses API adapter (llm.Client).
//
// Single-file ownership: streaming, message translation (straight-through; the
// Responses API accepts arbitrary role sequences), and error classification.
// API key is never embedded in returned error strings.
//
// Wire events map onto the unified stream as: output_text.delta → TextDelta,
// reasoning_text.delta / reasoning_summary_text.delta → ThinkingDelta,
// function_call items → ToolCallStart / ToolCallDelta (arguments.delta) /
// ToolCallComplete (arguments.done), response.completed usage →
// StreamEnd.Usage.

package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
)

// openAIClient is the llm.Client backed by the OpenAI Responses API.
type openAIClient struct {
	sdk   openai.Client
	model string
}

var _ Client = (*openAIClient)(nil)

func newOpenAIClient(cfg config.Provider, httpClient *http.Client) Client {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithMaxRetries(0),
	}
	if httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	return &openAIClient{sdk: openai.NewClient(opts...), model: cfg.Model}
}

// Stream implements Client.
func (c *openAIClient) Stream(ctx context.Context, req Request) <-chan StreamEvent {
	ch := make(chan StreamEvent, channelBuffer)
	go func() {
		defer close(ch)
		send := func(ev StreamEvent) bool {
			select {
			case ch <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}
		finish := func(ev StreamEvent) {
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
		var usage UsageInfo
		var stopReason string

		// Per-item tool-call accumulation. Function calls stream their name
		// (output_item.added), then arguments (arguments.delta), then finish
		// (arguments.done). Tracked by output_index to tolerate interleaving.
		type pendingTool struct {
			name string
		}
		tools := map[int64]*pendingTool{}

		for stream.Next() {
			switch ev := stream.Current().AsAny().(type) {
			case responses.ResponseOutputItemAddedEvent:
				if fc, ok := ev.Item.AsAny().(responses.ResponseFunctionToolCall); ok {
					tools[ev.OutputIndex] = &pendingTool{name: fc.Name}
					if !send(ToolCallStart{ToolName: fc.Name, ToolID: fc.CallID}) {
						return
					}
				}
			case responses.ResponseFunctionCallArgumentsDeltaEvent:
				if !send(ToolCallDelta{Text: ev.Delta}) {
					return
				}
			case responses.ResponseFunctionCallArgumentsDoneEvent:
				toolID := ev.ItemID
				name := ""
				if t, ok := tools[ev.OutputIndex]; ok {
					name = t.name
				}
				if !send(ToolCallComplete{
					ToolID:    toolID,
					ToolName:  name,
					Arguments: parseToolArgs(ev.Arguments),
				}) {
					return
				}
				delete(tools, ev.OutputIndex)
			case responses.ResponseTextDeltaEvent:
				if !send(TextDelta{Text: ev.Delta}) {
					return
				}
			case responses.ResponseReasoningTextDeltaEvent, responses.ResponseReasoningSummaryTextDeltaEvent:
				var text string
				switch d := ev.(type) {
				case responses.ResponseReasoningTextDeltaEvent:
					text = d.Delta
				case responses.ResponseReasoningSummaryTextDeltaEvent:
					text = d.Delta
				}
				if !send(ThinkingDelta{Text: text}) {
					return
				}
			case responses.ResponseCompletedEvent:
				sawCompleted = true
				usage = UsageInfo{
					InputTokens:     int(ev.Response.Usage.InputTokens),
					OutputTokens:    int(ev.Response.Usage.OutputTokens),
					CacheReadTokens: int(ev.Response.Usage.InputTokensDetails.CachedTokens),
				}
				stopReason = string(ev.Response.Status)
			case responses.ResponseFailedEvent:
				detail := ev.Response.Error.Message
				code := string(ev.Response.Error.Code)
				if code != "" {
					detail = fmt.Sprintf("%s: %s", code, detail)
				}
				if detail == "" {
					detail = "response.failed"
				}
				failErr = classify(0, "", code, detail, errors.New(detail))
			case responses.ResponseIncompleteEvent:
				detail := string(ev.Response.IncompleteDetails.Reason)
				if detail == "" {
					detail = "response.incomplete"
				}
				failErr = classify(0, "", "", detail, errors.New(detail))
			case responses.ResponseErrorEvent:
				detail := strings.TrimSpace(ev.Message)
				if detail == "" {
					detail = ev.Code
				}
				failErr = classify(0, "", ev.Code, detail, errors.New(detail))
			default:
				// content_part.*, output_item.done, *.done etc. are ignored.
			}
		}

		switch {
		case ctx.Err() != nil:
			finish(StreamError{Err: ctx.Err()})
		case stream.Err() != nil:
			finish(StreamError{Err: mapOpenAIError(stream.Err())})
		case failErr != nil:
			finish(StreamError{Err: failErr})
		case sawCompleted:
			finish(StreamEnd{StopReason: stopReason, Usage: usage})
		default:
			finish(StreamError{Err: errors.New("stream ended unexpectedly")})
		}
	}()
	return ch
}

func (c *openAIClient) buildParams(req Request) responses.ResponseNewParams {
	items := make(responses.ResponseInputParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := responses.EasyInputMessageRoleUser
		if m.Role == conversation.RoleAssistant {
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

// mapOpenAIError classifies a transport/API error returned by the SDK's
// stream.Err() (typically an apierror with HTTP status) onto the five kinds.
func mapOpenAIError(err error) error {
	if err == nil {
		return nil
	}
	status, code, msg := extractOpenAIMeta(err)
	if status == 0 {
		s := strings.ToLower(err.Error())
		switch {
		case strings.Contains(s, "401") || strings.Contains(s, "403") || strings.Contains(s, "authentication"):
			status = 401
		case strings.Contains(s, "429") || strings.Contains(s, "rate limit"):
			status = 429
		}
		if status == 0 && isNetworkHint(err) {
			return &NetworkError{Message: msgOrErr(msg, err), Cause: err}
		}
	}
	return classify(status, "", code, msgOrErr(msg, err), err)
}

func extractOpenAIMeta(err error) (status int, code, msg string) {
	var ae *openai.Error
	if errors.As(err, &ae) {
		status = ae.StatusCode
		code = ae.Code
	}
	msg = err.Error()
	return status, code, msg
}
