package llm

import (
	"context"
	"encoding/json"
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
	"github.com/FrexCheat/aether-code/internal/tools"
)

// openAIClient is the llm.Client backed by the OpenAI Responses API.
type openAIClient struct {
	sdk          openai.Client
	model        string
	systemPrompt string
	thinking     bool
}

var _ Client = (*openAIClient)(nil)

func newOpenAIClient(cfg config.Provider, httpClient *http.Client, systemPrompt string) Client {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithMaxRetries(0),
	}
	if httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	return &openAIClient{
		sdk:          openai.NewClient(opts...),
		model:        cfg.Model,
		systemPrompt: systemPrompt,
		thinking:     cfg.Thinking,
	}
}

// Stream implements Client.
func (c *openAIClient) Stream(ctx context.Context, req Request) <-chan StreamEvent {
	ch := make(chan StreamEvent, channelBuffer)
	go func() {
		defer close(ch)
		send := func(ev StreamEvent) bool { return forwardEvent(ctx, ch, ev) }
		finish := func(ev StreamEvent) { finishEvent(ctx, ch, ev) }

		stream := c.sdk.Responses.NewStreaming(ctx, c.buildParams(req))
		defer stream.Close()

		sawCompleted := false
		var failErr error
		var usage Usage

		// Function calls stream their name (output_item.added) then their
		// arguments (arguments.delta → done), tracked by output_index to
		// tolerate interleaving.
		pendingCalls := map[int64]string{}

		// inText tracks the message item carrying the visible reply; its
		// output_item.done is the unified TextComplete.
		inText := false

		// Per-item reasoning accumulation: the item's ID (captured at
		// output_item.added) becomes the ThinkingComplete signature so the
		// agent can replay it as a reasoning item on the next request.
		var (
			inReasoning   bool
			reasoningID   string
			reasoningText strings.Builder
		)
		flushThinking := func() bool {
			if !inReasoning {
				return true
			}
			inReasoning = false
			if !send(ThinkingComplete{Thinking: reasoningText.String(), Signature: reasoningID}) {
				return false
			}
			reasoningID = ""
			reasoningText.Reset()
			return true
		}

		for stream.Next() {
			switch ev := stream.Current().AsAny().(type) {
			case responses.ResponseOutputItemAddedEvent:
				switch item := ev.Item.AsAny().(type) {
				case responses.ResponseOutputMessage:
					inText = true
				case responses.ResponseFunctionToolCall:
					pendingCalls[ev.OutputIndex] = item.Name
				case responses.ResponseReasoningItem:
					inReasoning = true
					reasoningID = item.ID
					reasoningText.Reset()
				}
			case responses.ResponseOutputItemDoneEvent:
				switch ev.Item.AsAny().(type) {
				case responses.ResponseOutputMessage:
					if inText {
						inText = false
						if !send(TextComplete{}) {
							return
						}
					}
				case responses.ResponseReasoningItem:
					// Belt-and-braces: normally the *.done text events already
					// closed the segment; emit whatever accumulated if not.
					if !flushThinking() {
						return
					}
				}
			case responses.ResponseFunctionCallArgumentsDeltaEvent:
				// Argument fragments are ignored; the done event carries the
				// complete arguments.
			case responses.ResponseFunctionCallArgumentsDoneEvent:
				if !send(ToolCallComplete{
					ToolID:    ev.ItemID,
					ToolName:  pendingCalls[ev.OutputIndex],
					Arguments: parseToolArgs(ev.Arguments),
				}) {
					return
				}
				delete(pendingCalls, ev.OutputIndex)
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
				inReasoning = true
				reasoningText.WriteString(text)
				if !send(ThinkingDelta{Text: text}) {
					return
				}
			// Whichever done event arrives first closes the segment; the
			// inReasoning guard keeps the second one from double-emitting.
			case responses.ResponseReasoningTextDoneEvent, responses.ResponseReasoningSummaryTextDoneEvent:
				if !flushThinking() {
					return
				}
			case responses.ResponseCompletedEvent:
				sawCompleted = true
				usage = openAIUsage(ev.Response.Usage)
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
			finish(StreamEnd{Usage: usage})
		default:
			finish(StreamError{Err: errors.New("stream ended unexpectedly")})
		}
	}()
	return ch
}

func openAIUsage(u responses.ResponseUsage) Usage {
	cached := int(u.InputTokensDetails.CachedTokens)
	input := max(0, int(u.InputTokens)-cached)
	return Usage{
		InputTokens:     input,
		OutputTokens:    int(u.OutputTokens),
		CacheReadTokens: cached,
	}
}

func (c *openAIClient) buildParams(req Request) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(c.model),
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: buildOpenAIMessages(req.Messages)},
	}
	if c.systemPrompt != "" {
		params.Instructions = openai.String(c.systemPrompt)
	}
	if c.thinking {
		params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffortMedium}
	}
	if len(req.Tools) > 0 {
		params.Tools = translateOpenAITools(req.Tools)
	}
	return params
}

// The Responses API tolerates arbitrary role sequences, so each message maps to
// its items without merging; only thinking blocks and tool calls are re-encoded.
func buildOpenAIMessages(messages []conversation.Message) responses.ResponseInputParam {
	items := make(responses.ResponseInputParam, 0, len(messages))
	for _, m := range messages {
		if m.Role == conversation.RoleAssistant {
			for _, tb := range m.ThinkingBlocks {
				items = append(items, responses.ResponseInputItemParamOfReasoning(
					tb.Signature,
					[]responses.ResponseReasoningItemSummaryParam{{Text: tb.Text}},
				))
			}
			if m.Content != "" {
				items = append(items, responses.ResponseInputItemParamOfMessage(
					m.Content, responses.EasyInputMessageRoleAssistant))
			}
			for _, tu := range m.ToolUses {
				// Args go back as a JSON string; encoding/json sorts map keys,
				// so replays are byte-stable.
				argsJSON, err := json.Marshal(tu.Args)
				if err != nil {
					argsJSON = []byte("{}")
				}
				items = append(items,
					responses.ResponseInputItemParamOfFunctionCall(string(argsJSON), tu.ID, tu.Name))
			}
			continue
		}
		for _, tr := range m.ToolResults {
			item := responses.ResponseInputItemParamOfFunctionCallOutput(tr.Content)
			item.OfFunctionCallOutput.CallID = openai.String(tr.ToolUseID)
			items = append(items, item)
		}
		if m.Content != "" {
			items = append(items, responses.ResponseInputItemParamOfMessage(
				m.Content, responses.EasyInputMessageRoleUser))
		}
	}
	return items
}

func translateOpenAITools(schemas []tools.ToolSchema) []responses.ToolUnionParam {
	result := make([]responses.ToolUnionParam, 0, len(schemas))
	for _, s := range schemas {
		schema := s.InputSchema
		if schema == nil {
			schema = map[string]any{}
		}
		ft := responses.FunctionToolParam{
			Name:       s.Name,
			Parameters: schema,
			Strict:     openai.Bool(false),
		}
		if s.Description != "" {
			ft.Description = openai.String(s.Description)
		}
		result = append(result, responses.ToolUnionParam{OfFunction: &ft})
	}
	return result
}

func mapOpenAIError(err error) error {
	if err == nil {
		return nil
	}
	status, code, message := extractOpenAIMeta(err)
	if status == 0 {
		s := strings.ToLower(err.Error())
		switch {
		case strings.Contains(s, "401") || strings.Contains(s, "403") || strings.Contains(s, "authentication"):
			status = 401
		case strings.Contains(s, "429") || strings.Contains(s, "rate limit"):
			status = 429
		}
		if status == 0 && isNetworkHint(err) {
			return &NetworkError{Message: message, Cause: err}
		}
	}
	return classify(status, "", code, message, err)
}

func extractOpenAIMeta(err error) (status int, code, message string) {
	if ae, ok := errors.AsType[*openai.Error](err); ok {
		status = ae.StatusCode
		code = ae.Code
	}
	message = err.Error()
	return status, code, message
}
