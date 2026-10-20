package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
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
	maxOutput    int
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
		maxOutput:    cfg.MaxOutputTokens,
	}
}

// Stream implements Client.
func (c *openAIClient) Stream(ctx context.Context, req Request) iter.Seq2[StreamEvent, error] {
	return func(yield func(StreamEvent, error) bool) {
		stream := c.sdk.Responses.NewStreaming(ctx, c.buildParams(req))
		defer stream.Close()
		send := func(event StreamEvent) bool {
			return yield(event, nil)
		}

		sawCompleted := false
		var failErr error
		var usage Usage

		// Function calls stream their name and call id (output_item.added)
		// then their arguments (arguments.delta → done), tracked by
		// output_index to tolerate interleaving. The call id, not the item id,
		// is what the function_call_output must name.
		type pendingCall struct{ name, callID string }
		pendingCalls := map[int64]pendingCall{}

		// inText tracks the message item carrying the visible reply; its
		// output_item.done is the unified TextComplete.
		inText := false

		// Per-item reasoning accumulation: the deltas feed the live
		// ThinkingDelta stream and stand in for a stream that dies before the
		// completed item, but the block handed to the agent comes from
		// output_item.done — only that copy is complete, and the API requires
		// the reasoning text back verbatim on the next request.
		var (
			inReasoning      bool
			reasoningID      string
			reasoningContent strings.Builder
			reasoningSummary strings.Builder
		)
		resetReasoning := func() {
			inReasoning = false
			reasoningID = ""
			reasoningContent.Reset()
			reasoningSummary.Reset()
		}

		for stream.Next() {
			switch ev := stream.Current().AsAny().(type) {
			case responses.ResponseOutputItemAddedEvent:
				switch item := ev.Item.AsAny().(type) {
				case responses.ResponseOutputMessage:
					inText = true
				case responses.ResponseFunctionToolCall:
					pendingCalls[ev.OutputIndex] = pendingCall{name: item.Name, callID: item.CallID}
				case responses.ResponseReasoningItem:
					// A response may carry several reasoning items; each
					// starts its own accumulation.
					inReasoning = true
					reasoningID = item.ID
					reasoningContent.Reset()
					reasoningSummary.Reset()
				}
			case responses.ResponseOutputItemDoneEvent:
				switch item := ev.Item.AsAny().(type) {
				case responses.ResponseOutputMessage:
					if inText {
						inText = false
						if !send(TextComplete{}) {
							return
						}
					}
				case responses.ResponseReasoningItem:
					// The completed item is the authoritative copy, encrypted
					// payload included; the delta accumulators only stand in
					// for it.
					if !send(ThinkingComplete{
						Thinking:  joinReasoningSummary(item.Summary),
						Content:   joinReasoningContent(item.Content),
						Signature: item.ID,
						Encrypted: item.EncryptedContent,
					}) {
						return
					}
					resetReasoning()
				}
			case responses.ResponseFunctionCallArgumentsDeltaEvent:
				// Argument fragments are ignored; the done event carries the
				// complete arguments.
			case responses.ResponseFunctionCallArgumentsDoneEvent:
				call := pendingCalls[ev.OutputIndex]
				id := call.callID
				if id == "" {
					// A provider that never named a call id: an item id keeps
					// the call and its output paired.
					id = ev.ItemID
				}
				if !send(ToolCallComplete{
					ToolID:    id,
					ToolName:  call.name,
					Arguments: parseToolArgs(ev.Arguments),
				}) {
					return
				}
				delete(pendingCalls, ev.OutputIndex)
			case responses.ResponseTextDeltaEvent:
				if !send(TextDelta{Text: ev.Delta}) {
					return
				}
			case responses.ResponseReasoningTextDeltaEvent:
				inReasoning = true
				reasoningContent.WriteString(ev.Delta)
				if !send(ThinkingDelta{Kind: ThinkingContent, Text: ev.Delta}) {
					return
				}
			case responses.ResponseReasoningSummaryTextDeltaEvent:
				inReasoning = true
				reasoningSummary.WriteString(ev.Delta)
				if !send(ThinkingDelta{Kind: ThinkingSummary, Text: ev.Delta}) {
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

		if inReasoning {
			// The stream ended before the completed item: hand the agent what
			// the deltas accumulated rather than dropping the reasoning.
			if !send(ThinkingComplete{
				Thinking:  reasoningSummary.String(),
				Content:   reasoningContent.String(),
				Signature: reasoningID,
			}) {
				return
			}
			resetReasoning()
		}

		if err := streamResult(ctx, mapOpenAIError(stream.Err()), failErr, sawCompleted); err != nil {
			yield(nil, err)
			return
		}
		yield(StreamEnd{Usage: usage}, nil)
	}
}

// joinReasoningSummary and joinReasoningContent flatten a reasoning item's
// text parts; a provider mid-turn sends at most one of each channel.
func joinReasoningSummary(parts []responses.ResponseReasoningItemSummary) string {
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(part.Text)
	}
	return b.String()
}

func joinReasoningContent(parts []responses.ResponseReasoningItemContent) string {
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(part.Text)
	}
	return b.String()
}

func openAIUsage(u responses.ResponseUsage) Usage {
	cached := int(u.InputTokensDetails.CachedTokens)
	written := int(u.InputTokensDetails.CacheWriteTokens)
	// input_tokens is inclusive on this wire: both cache details are subsets,
	// subtracted so InputTokens keeps the package-wide "excludes cache" shape.
	input := max(0, int(u.InputTokens)-cached-written)
	return Usage{
		InputTokens:         input,
		OutputTokens:        int(u.OutputTokens),
		CacheReadTokens:     cached,
		CacheCreationTokens: written,
	}
}

func (c *openAIClient) buildParams(req Request) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model:           shared.ResponsesModel(c.model),
		Input:           responses.ResponseNewParamsInputUnion{OfInputItemList: buildOpenAIMessages(req.Messages)},
		MaxOutputTokens: param.NewOpt(int64(c.maxOutput)),
	}
	if c.systemPrompt != "" {
		params.Instructions = openai.String(c.systemPrompt)
	}
	if c.thinking {
		params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffortMedium}
	} else {
		params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffortNone}
	}
	if len(req.Tools) > 0 {
		params.Tools = translateOpenAITools(req.Tools)
	}
	return params
}

// reasoningItem re-encodes a captured reasoning block. Thinking-mode endpoints
// reject a replayed tool turn whose reasoning_text is missing — the model's own
// reasoning must come back — so every part we captured travels verbatim, and
// the empty ones are omitted.
func reasoningItem(tb conversation.ThinkingBlock) responses.ResponseInputItemUnionParam {
	item := &responses.ResponseReasoningItemParam{ID: tb.Signature}
	if tb.Text != "" {
		item.Summary = []responses.ResponseReasoningItemSummaryParam{{Text: tb.Text}}
	}
	if tb.Content != "" {
		item.Content = []responses.ResponseReasoningItemContentParam{{Text: tb.Content}}
	}
	if tb.Encrypted != "" {
		item.EncryptedContent = param.NewOpt(tb.Encrypted)
	}
	return responses.ResponseInputItemUnionParam{OfReasoning: item}
}

// The Responses API tolerates arbitrary role sequences, so each message maps to
// its items without merging; only thinking blocks and tool calls are re-encoded.
func buildOpenAIMessages(messages []conversation.Message) responses.ResponseInputParam {
	items := make(responses.ResponseInputParam, 0, len(messages))
	for _, m := range messages {
		if m.Role == conversation.RoleAssistant {
			for _, tb := range m.ThinkingBlocks {
				items = append(items, reasoningItem(tb))
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
			// ToolRefs is ignored: the Output text already carries what the
			// result surfaced, which is all the Responses API can accept.
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
	// Schema.Deferred is deliberately unread: the Responses API has no
	// deferred-loading notion, and deferred modes never activate on this
	// protocol.
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
		status = statusHint(message)
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
