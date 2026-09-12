// anthropic.go — Anthropic Messages API adapter (llm.Client).
//
// Single-file ownership: streaming, message translation (including consecutive
// same-role merging per the API docs), and error classification. API key is
// never embedded in returned error strings.
//
// Wire events map onto the unified stream as: text_delta → TextDelta,
// thinking_delta → ThinkingDelta, signature_delta accumulates into
// ThinkingComplete at content_block_stop, tool_use blocks → ToolCallStart /
// ToolCallDelta (input_json_delta) / ToolCallComplete, message_delta usage →
// StreamEnd.Usage.

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
)

const (
	anthropicMaxTokens      = 16384
	anthropicThinkingBudget = 8192
	channelBuffer           = 64
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

		params := c.buildParams(req)
		stream := c.sdk.Messages.NewStreaming(ctx, params)
		defer stream.Close()

		sawStop := false
		var usage anthropicUsage
		var stopReason string

		// Per-block accumulation. Anthropic streams blocks sequentially:
		// start → deltas → stop before the next block starts.
		var (
			curThinking strings.Builder
			curSig      string
			curToolID   string
			curToolName string
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
				case anthropic.ThinkingBlock:
					curThinking.Reset()
					curSig = block.Signature
				case anthropic.ToolUseBlock:
					curToolID, curToolName = block.ID, block.Name
					curToolSeed = block.Input
					curToolArgs.Reset()
					if !send(ToolCallStart{ToolName: block.Name, ToolID: block.ID}) {
						return
					}
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
					if !send(ToolCallDelta{Text: d.PartialJSON}) {
						return
					}
				}
			case anthropic.ContentBlockStopEvent:
				if curThinking.Len() > 0 {
					if !send(ThinkingComplete{Thinking: curThinking.String(), Signature: curSig}) {
						return
					}
					curThinking.Reset()
					curSig = ""
				}
				if curToolID != "" {
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
				stopReason = string(ev.Delta.StopReason)
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
			finish(StreamEnd{StopReason: stopReason, Usage: UsageInfo{
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
	merged := mergeConsecutive(req.Messages)
	msgs := make([]anthropic.MessageParam, 0, len(merged))
	for _, m := range merged {
		blocks := translateBlocks(m)
		if m.Role == conversation.RoleAssistant {
			msgs = append(msgs, anthropic.NewAssistantMessage(blocks...))
		} else {
			msgs = append(msgs, anthropic.NewUserMessage(blocks...))
		}
	}
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: anthropicMaxTokens,
		Messages:  msgs,
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	if req.Thinking {
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(anthropicThinkingBudget)
	}
	if len(req.Tools) > 0 {
		params.Tools = translateTools(req.Tools)
	}
	return params
}

// translateBlocks converts one unified message into Anthropic content blocks.
// Messages without blocks keep the plain-text path; block messages translate
// each fragment by type.
func translateBlocks(m conversation.Message) []anthropic.ContentBlockParamUnion {
	if len(m.Blocks) == 0 {
		return []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(m.Content)}
	}
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.Blocks))
	for _, b := range m.Blocks {
		switch b := b.(type) {
		case conversation.TextBlock:
			blocks = append(blocks, anthropic.NewTextBlock(b.Text))
		case conversation.ThinkingBlock:
			blocks = append(blocks, anthropic.NewThinkingBlock(b.Signature, b.Text))
		case conversation.ToolUseBlock:
			blocks = append(blocks, anthropic.NewToolUseBlock(b.ID, b.Args, b.Name))
		case conversation.ToolResultBlock:
			blocks = append(blocks, anthropic.NewToolResultBlock(b.ToolUseID, b.Content, b.IsError))
		}
	}
	return blocks
}

// translateTools converts the neutral schema shape into Anthropic tool params.
func translateTools(tools []map[string]any) []anthropic.ToolUnionParam {
	result := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		tool := anthropic.ToolParam{Name: t["name"].(string)}
		if d, ok := t["description"].(string); ok {
			tool.Description = anthropic.String(d)
		}
		if schema, ok := t["input_schema"].(map[string]any); ok {
			input := anthropic.ToolInputSchemaParam{Type: "object"}
			if props, ok := schema["properties"]; ok {
				input.Properties = props
			}
			switch reqs := schema["required"].(type) {
			case []string:
				input.Required = reqs
			case []any:
				for _, r := range reqs {
					if s, ok := r.(string); ok {
						input.Required = append(input.Required, s)
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

// mergeConsecutive collapses consecutive messages of the same role, as
// required by the Anthropic API docs ("Consecutive user or assistant turns
// will be combined into a single turn"). Text-only messages join Content with
// "\n\n"; block-carrying messages concatenate their block lists, keeping the
// tool_use/tool_result pairing intact.
func mergeConsecutive(in []conversation.Message) []conversation.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]conversation.Message, 0, len(in))
	cur := in[0]
	for _, m := range in[1:] {
		if m.Role != cur.Role {
			out = append(out, cur)
			cur = m
			continue
		}
		switch {
		case len(m.Blocks) == 0:
			if len(cur.Blocks) == 0 {
				cur.Content += "\n\n" + m.Content
			} else {
				cur.Blocks = append(cur.Blocks, conversation.TextBlock{Text: m.Content})
			}
		case len(cur.Blocks) == 0 && cur.Content == "":
			cur.Blocks = m.Blocks
		case len(cur.Blocks) == 0:
			cur.Blocks = append([]conversation.Block{conversation.TextBlock{Text: cur.Content}}, m.Blocks...)
			cur.Content = ""
		default:
			cur.Blocks = append(cur.Blocks, m.Blocks...)
		}
	}
	out = append(out, cur)
	return out
}

// parseToolArgs decodes the accumulated tool-use JSON into a map. Malformed
// (e.g. truncated) input yields an empty map rather than failing the stream.
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
	status, apiType, apiCode, msg := extractAnthropicMeta(err)
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
	return classify(status, apiType, apiCode, msgOrErr(msg, err), err)
}

func extractAnthropicMeta(err error) (status int, apiType, apiCode, msg string) {
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
	_ = apiCode
	return status, apiType, "", msg
}

func msgOrErr(msg string, err error) string {
	if msg != "" {
		return msg
	}
	return err.Error()
}
