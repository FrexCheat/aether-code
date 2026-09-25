package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/tools"
)

const (
	anthropicMaxTokens      = 16384
	anthropicThinkingBudget = 8192
	// nativeToolSearchBeta is the beta that honors defer_loading on tools and
	// tool_reference blocks in tool results. The SDK knows neither; requests
	// carrying deferred tools pin it here.
	nativeToolSearchBeta = "advanced-tool-use-2025-11-20"
)

// anthropicClient is the llm.Client backed by the Anthropic Messages API.
type anthropicClient struct {
	sdk          anthropic.Client
	model        string
	systemPrompt string
	thinking     bool
}

var _ Client = (*anthropicClient)(nil)

func newAnthropicClient(cfg config.Provider, httpClient *http.Client, systemPrompt string) Client {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithMaxRetries(0),
	}
	if httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	return &anthropicClient{
		sdk:          anthropic.NewClient(opts...),
		model:        cfg.Model,
		systemPrompt: systemPrompt,
		thinking:     cfg.Thinking,
	}
}

// Stream implements Client.
func (c *anthropicClient) Stream(ctx context.Context, req Request) <-chan StreamEvent {
	ch := make(chan StreamEvent, channelBuffer)
	go func() {
		defer close(ch)
		send := func(ev StreamEvent) bool { return forwardEvent(ctx, ch, ev) }
		finish := func(ev StreamEvent) { finishEvent(ctx, ch, ev) }

		params := c.buildParams(req)
		var opts []option.RequestOption
		if slices.ContainsFunc(req.Tools, func(t tools.ToolSchema) bool { return t.Deferred }) {
			opts = append(opts, option.WithHeader("anthropic-beta", nativeToolSearchBeta))
		}
		stream := c.sdk.Messages.NewStreaming(ctx, params, opts...)
		defer stream.Close()

		sawStop := false
		var usage Usage

		// Per-block accumulation. Anthropic streams blocks sequentially:
		// start → deltas → stop before the next block starts.
		var (
			thinkingAccum     strings.Builder
			thinkingSignature string
			toolCallID        string
			toolCallName      string
			inText            bool // inside a text block; TextComplete fires at its stop
			toolArgsSeed      json.RawMessage
			toolArgsJSON      strings.Builder
		)

		for stream.Next() {
			switch ev := stream.Current().AsAny().(type) {
			case anthropic.ContentBlockStartEvent:
				switch block := ev.ContentBlock.AsAny().(type) {
				case anthropic.TextBlock:
					inText = true
				case anthropic.ThinkingBlock:
					thinkingAccum.Reset()
					thinkingSignature = block.Signature
				case anthropic.ToolUseBlock:
					toolCallID, toolCallName = block.ID, block.Name
					toolArgsSeed = block.Input
					toolArgsJSON.Reset()
				}
			case anthropic.ContentBlockDeltaEvent:
				switch d := ev.Delta.AsAny().(type) {
				case anthropic.TextDelta:
					if !send(TextDelta{Text: d.Text}) {
						return
					}
				case anthropic.ThinkingDelta:
					thinkingAccum.WriteString(d.Thinking)
					if !send(ThinkingDelta{Text: d.Thinking}) {
						return
					}
				case anthropic.SignatureDelta:
					thinkingSignature = d.Signature
				case anthropic.InputJSONDelta:
					toolArgsJSON.WriteString(d.PartialJSON)
				}
			case anthropic.ContentBlockStopEvent:
				switch {
				case inText:
					inText = false
					if !send(TextComplete{}) {
						return
					}
				case thinkingAccum.Len() > 0:
					if !send(ThinkingComplete{Thinking: thinkingAccum.String(), Signature: thinkingSignature}) {
						return
					}
					thinkingAccum.Reset()
					thinkingSignature = ""
				case toolCallID != "":
					// Assemble the fragmented input_json_delta slices; the
					// block-start input is only a "{}" placeholder on the
					// streaming wire, so it is a fallback when no deltas arrived.
					raw := toolArgsJSON.String()
					if strings.TrimSpace(raw) == "" {
						raw = string(toolArgsSeed)
					}
					if !send(ToolCallComplete{
						ToolID:    toolCallID,
						ToolName:  toolCallName,
						Arguments: parseToolArgs(raw),
					}) {
						return
					}
					toolCallID = ""
					toolArgsSeed = nil
				}
			case anthropic.MessageStartEvent:
				usage = mergeUsage(usage, anthropicUsage(
					ev.Message.Usage.InputTokens,
					ev.Message.Usage.OutputTokens,
					ev.Message.Usage.CacheReadInputTokens,
					ev.Message.Usage.CacheCreationInputTokens,
				))
			case anthropic.MessageDeltaEvent:
				// Some gateways report the input side only in message_delta
				// instead of message_start; merging keeps whichever event
				// actually carried each field.
				usage = mergeUsage(usage, anthropicUsage(
					ev.Usage.InputTokens,
					ev.Usage.OutputTokens,
					ev.Usage.CacheReadInputTokens,
					ev.Usage.CacheCreationInputTokens,
				))
			case anthropic.MessageStopEvent:
				sawStop = true
			}
		}

		switch {
		case ctx.Err() != nil:
			finish(StreamError{Err: ctx.Err()})
		case stream.Err() != nil:
			finish(StreamError{Err: mapAnthropicError(stream.Err())})
		case sawStop:
			finish(StreamEnd{Usage: usage})
		default:
			finish(StreamError{Err: errors.New("stream ended unexpectedly")})
		}
	}()
	return ch
}

func anthropicUsage(input, output, cacheRead, cacheWrite int64) Usage {
	return Usage{
		InputTokens:         int(input),
		OutputTokens:        int(output),
		CacheReadTokens:     int(cacheRead),
		CacheCreationTokens: int(cacheWrite),
	}
}

// mergeUsage folds a later event's counts into the running total, keeping the
// larger value per field: the wire counts are cumulative and never decrease,
// so the maximum is also the most recently reported truth.
func mergeUsage(total, next Usage) Usage {
	return Usage{
		InputTokens:         max(total.InputTokens, next.InputTokens),
		OutputTokens:        max(total.OutputTokens, next.OutputTokens),
		CacheReadTokens:     max(total.CacheReadTokens, next.CacheReadTokens),
		CacheCreationTokens: max(total.CacheCreationTokens, next.CacheCreationTokens),
	}
}

func (c *anthropicClient) buildParams(req Request) anthropic.MessageNewParams {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: anthropicMaxTokens,
		Messages:  buildAnthropicMessages(req.Messages),
	}
	if c.systemPrompt != "" {
		// Cache breakpoint #1: the system block. The API's cache prefix is
		// ordered tools → system → messages, so this one breakpoint also covers
		// the tool definitions — both are re-sent on every turn of the loop.
		params.System = []anthropic.TextBlockParam{{
			Text:         c.systemPrompt,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}}
	}
	if c.thinking {
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(anthropicThinkingBudget)
	}
	if len(req.Tools) > 0 {
		params.Tools = translateAnthropicTools(req.Tools)
	}
	// Cache breakpoint #2: the tail of the conversation, re-marked on every
	// request so it follows the history as it grows.
	markMessageTailForCache(params.Messages)
	return params
}

func markMessageTailForCache(msgs []anthropic.MessageParam) {
	for i := range slices.Backward(msgs) {
		if msgs[i].Role != anthropic.MessageParamRoleUser || len(msgs[i].Content) == 0 {
			continue
		}
		last := &msgs[i].Content[len(msgs[i].Content)-1]
		switch {
		case last.OfText != nil:
			last.OfText.CacheControl = anthropic.NewCacheControlEphemeralParam()
		case last.OfToolResult != nil:
			last.OfToolResult.CacheControl = anthropic.NewCacheControlEphemeralParam()
		}
		return
	}
}

func buildAnthropicMessages(messages []conversation.Message) []anthropic.MessageParam {
	var out []anthropic.MessageParam
	for _, m := range messages {
		var role anthropic.MessageParamRole
		var blocks []anthropic.ContentBlockParamUnion
		if m.Role == conversation.RoleAssistant {
			role = anthropic.MessageParamRoleAssistant
			for _, tb := range m.ThinkingBlocks {
				blocks = append(blocks, anthropic.NewThinkingBlock(tb.Signature, tb.Text))
			}
			if m.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Content))
			}
			for _, tu := range m.ToolUses {
				blocks = append(blocks, anthropic.NewToolUseBlock(tu.ID, tu.Args, tu.Name))
			}
			if len(blocks) == 0 {
				blocks = append(blocks, anthropic.NewTextBlock(""))
			}
		} else {
			role = anthropic.MessageParamRoleUser
			for _, tr := range m.ToolResults {
				if len(tr.ToolRefs) == 0 {
					blocks = append(blocks, anthropic.NewToolResultBlock(tr.ToolUseID, tr.Content, tr.IsError))
					continue
				}
				// A ToolSearch result: the text plus one tool_reference per
				// surfaced tool, which the API expands into full definitions.
				content := []anthropic.ToolResultBlockParamContentUnion{
					{OfText: &anthropic.TextBlockParam{Text: tr.Content}},
				}
				for _, name := range tr.ToolRefs {
					content = append(content, anthropic.ToolResultBlockParamContentUnion{
						OfToolReference: &anthropic.ToolReferenceBlockParam{ToolName: name},
					})
				}
				blocks = append(blocks, anthropic.ContentBlockParamUnion{
					OfToolResult: &anthropic.ToolResultBlockParam{
						ToolUseID: tr.ToolUseID,
						Content:   content,
						IsError:   anthropic.Bool(tr.IsError),
					},
				})
			}
			if m.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Content))
			}
			if len(blocks) == 0 {
				blocks = append(blocks, anthropic.NewTextBlock(""))
			}
		}

		out = append(out, anthropic.MessageParam{Role: role, Content: blocks})
	}
	return out
}

func translateAnthropicTools(schemas []tools.ToolSchema) []anthropic.ToolUnionParam {
	result := make([]anthropic.ToolUnionParam, 0, len(schemas))
	for _, s := range schemas {
		tool := anthropic.ToolParam{Name: s.Name}
		if s.Deferred {
			tool.DeferLoading = anthropic.Bool(true)
		}
		if s.Description != "" {
			tool.Description = anthropic.String(s.Description)
		}
		if schema := s.InputSchema; schema != nil {
			input := anthropic.ToolInputSchemaParam{Type: "object"}
			if props, ok := schema["properties"]; ok {
				input.Properties = props
			}
			switch reqs := schema["required"].(type) {
			case []string:
				input.Required = reqs
			case []any:
				for _, r := range reqs {
					if str, ok := r.(string); ok {
						input.Required = append(input.Required, str)
					}
				}
			}
			for k, v := range schema {
				switch k {
				case "properties", "required", "type":
				default:
					if input.ExtraFields == nil {
						input.ExtraFields = map[string]any{}
					}
					input.ExtraFields[k] = v
				}
			}
			tool.InputSchema = input
		}
		result = append(result, anthropic.ToolUnionParam{OfTool: &tool})
	}
	return result
}

func mapAnthropicError(err error) error {
	if err == nil {
		return nil
	}
	status, apiType, message := extractAnthropicMeta(err)
	if message == "" {
		message = err.Error()
	}
	if status == 0 {
		s := strings.ToLower(err.Error())
		switch {
		case strings.Contains(s, "401") || strings.Contains(s, "403") || strings.Contains(s, "authentication"):
			status = 401
		case strings.Contains(s, "429") || strings.Contains(s, "rate limit"):
			status = 429
		case strings.Contains(s, "overloaded") || strings.Contains(s, "529"):
			return &ServerError{Message: message, Cause: err}
		}
		if status == 0 && isNetworkHint(err) {
			return &NetworkError{Message: message, Cause: err}
		}
	}
	return classify(status, apiType, "", message, err)
}

func extractAnthropicMeta(err error) (status int, apiType, message string) {
	if ae, ok := errors.AsType[*anthropic.Error](err); ok {
		status = ae.StatusCode
	}
	message = err.Error()
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "overloaded_error"):
		apiType = "overloaded_error"
	case strings.Contains(lower, "rate_limit"):
		apiType = "rate_limit_error"
	case strings.Contains(lower, "authentication_error"):
		apiType = "authentication_error"
	case strings.Contains(lower, "invalid_request"):
		apiType = "invalid_request_error"
	}
	return status, apiType, message
}
