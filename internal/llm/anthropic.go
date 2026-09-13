// anthropic.go — Anthropic Messages API adapter (llm.Client).
//
// Streaming, message translation (including consecutive same-role merging per
// the API docs), and error classification; delivery and arg-parsing helpers
// are shared in stream.go. API key is never embedded in returned error
// strings.
//
// Wire events map onto the unified stream as: text_delta → TextDelta with the
// text block's content_block_stop → TextComplete, thinking_delta →
// ThinkingDelta, signature_delta accumulates into ThinkingComplete at
// content_block_stop, tool_use blocks accumulate input_json_delta into one
// ToolCallComplete, message_delta usage → StreamEnd.Usage.

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
)

// anthropicClient is the llm.Client backed by the Anthropic Messages API.
type anthropicClient struct {
	sdk   anthropic.Client
	model string
}

var _ Client = (*anthropicClient)(nil)

func newAnthropicClient(cfg config.Provider, httpClient *http.Client) Client {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithMaxRetries(0),
	}
	if httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	return &anthropicClient{sdk: anthropic.NewClient(opts...), model: cfg.Model}
}

// Stream implements Client.
func (c *anthropicClient) Stream(ctx context.Context, req Request) <-chan StreamEvent {
	ch := make(chan StreamEvent, channelBuffer)
	go func() {
		defer close(ch)
		send := func(ev StreamEvent) bool { return forwardEvent(ctx, ch, ev) }
		finish := func(ev StreamEvent) { finishEvent(ctx, ch, ev) }

		params := c.buildParams(req)
		stream := c.sdk.Messages.NewStreaming(ctx, params)
		defer stream.Close()

		sawStop := false
		var usage anthropicUsage

		// Per-block accumulation. Anthropic streams blocks sequentially:
		// start → deltas → stop before the next block starts.
		var (
			curThinking strings.Builder
			curSig      string
			curToolID   string
			curToolName string
			curText     bool // inside a text block; TextComplete fires at its stop
			// curToolSeed holds the input delivered at content_block_start;
			// on the streaming wire it is only the "{}" placeholder, so the
			// real arguments always come from curToolArgs below. The seed is
			// a fallback for blocks whose input arrives complete at start.
			curToolSeed json.RawMessage
			curToolArgs strings.Builder
		)

		for stream.Next() {
			switch ev := stream.Current().AsAny().(type) {
			case anthropic.ContentBlockStartEvent:
				switch block := ev.ContentBlock.AsAny().(type) {
				case anthropic.TextBlock:
					curText = true
				case anthropic.ThinkingBlock:
					curThinking.Reset()
					curSig = block.Signature
				case anthropic.ToolUseBlock:
					curToolID, curToolName = block.ID, block.Name
					curToolSeed = block.Input
					curToolArgs.Reset()
				}
			case anthropic.ContentBlockDeltaEvent:
				switch d := ev.Delta.AsAny().(type) {
				case anthropic.TextDelta:
					if !send(TextDelta{Text: d.Text}) {
						return
					}
				case anthropic.ThinkingDelta:
					curThinking.WriteString(d.Thinking)
					if !send(ThinkingDelta{Text: d.Thinking}) {
						return
					}
				case anthropic.SignatureDelta:
					curSig = d.Signature
				case anthropic.InputJSONDelta:
					curToolArgs.WriteString(d.PartialJSON)
				}
			case anthropic.ContentBlockStopEvent:
				switch {
				case curText:
					curText = false
					if !send(TextComplete{}) {
						return
					}
				case curThinking.Len() > 0:
					if !send(ThinkingComplete{Thinking: curThinking.String(), Signature: curSig}) {
						return
					}
					curThinking.Reset()
					curSig = ""
				case curToolID != "":
					// Assemble the fragmented input_json_delta slices; fall
					// back to the block-start seed when no deltas arrived.
					raw := curToolArgs.String()
					if strings.TrimSpace(raw) == "" {
						raw = string(curToolSeed)
					}
					if !send(ToolCallComplete{
						ToolID:    curToolID,
						ToolName:  curToolName,
						Arguments: parseToolArgs(raw),
					}) {
						return
					}
					curToolID = ""
					curToolSeed = nil
				}
			case anthropic.MessageDeltaEvent:
				usage = anthropicUsage{
					input:  int(ev.Usage.InputTokens),
					output: int(ev.Usage.OutputTokens),
					cacheR: int(ev.Usage.CacheReadInputTokens),
					cacheW: int(ev.Usage.CacheCreationInputTokens),
				}
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
			finish(StreamEnd{Usage: UsageInfo{
				InputTokens:         usage.input,
				OutputTokens:        usage.output,
				CacheReadTokens:     usage.cacheR,
				CacheCreationTokens: usage.cacheW,
			}})
		default:
			finish(StreamError{Err: errors.New("stream ended unexpectedly")})
		}
	}()
	return ch
}

func (c *anthropicClient) buildParams(req Request) anthropic.MessageNewParams {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: anthropicMaxTokens,
		Messages:  buildAnthropicMessages(req.Messages),
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	if req.Thinking {
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(anthropicThinkingBudget)
	}
	if len(req.Tools) > 0 {
		params.Tools = translateAnthropicTools(req.Tools)
	}
	return params
}

// buildAnthropicMessages converts the unified history into Anthropic wire
// messages. Anthropic requires alternating roles: consecutive same-role turns
// are folded into one message by appending content blocks. Tool results ride
// in user-role messages immediately after the assistant turn that issued the
// calls; the assistant's thinking blocks are replayed first, signature
// included, so extended thinking stays valid across tool rounds.
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
				blocks = append(blocks, anthropic.NewToolResultBlock(tr.ToolUseID, tr.Content, tr.IsError))
			}
			if m.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Content))
			}
			if len(blocks) == 0 {
				blocks = append(blocks, anthropic.NewTextBlock(""))
			}
		}

		// Fold a same-role message into the previous one; for user messages
		// this covers the common tool-results-then-next-prompt sequence.
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			continue
		}
		out = append(out, anthropic.MessageParam{Role: role, Content: blocks})
	}
	return out
}

// translateAnthropicTools converts the neutral tool schemas into Anthropic
// tool params.
func translateAnthropicTools(schemas []tools.ToolSchema) []anthropic.ToolUnionParam {
	result := make([]anthropic.ToolUnionParam, 0, len(schemas))
	for _, s := range schemas {
		tool := anthropic.ToolParam{Name: s.Name}
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

// anthropicUsage collects the cumulative usage reported by message_delta.
type anthropicUsage struct {
	input, output, cacheR, cacheW int
}

// mapAnthropicError classifies a transport/API error returned by the SDK's
// stream.Err() (typically an apierror with HTTP status) onto the five kinds.
func mapAnthropicError(err error) error {
	if err == nil {
		return nil
	}
	status, apiType, msg := extractAnthropicMeta(err)
	if status == 0 {
		s := strings.ToLower(err.Error())
		switch {
		case strings.Contains(s, "401") || strings.Contains(s, "403") || strings.Contains(s, "authentication"):
			status = 401
		case strings.Contains(s, "429") || strings.Contains(s, "rate limit"):
			status = 429
		case strings.Contains(s, "overloaded") || strings.Contains(s, "529"):
			return &ServerError{Message: msgOrErr(msg, err), Cause: err}
		}
		if status == 0 && isNetworkHint(err) {
			return &NetworkError{Message: msgOrErr(msg, err), Cause: err}
		}
	}
	return classify(status, apiType, "", msgOrErr(msg, err), err)
}

func extractAnthropicMeta(err error) (status int, apiType, msg string) {
	var ae *anthropic.Error
	if errors.As(err, &ae) {
		status = ae.StatusCode
	}
	msg = err.Error()
	lower := strings.ToLower(msg)
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
	return status, apiType, msg
}

func msgOrErr(msg string, err error) string {
	if msg != "" {
		return msg
	}
	return err.Error()
}
