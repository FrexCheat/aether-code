package llm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
)

func openAITestClient(t *testing.T, body string) Client {
	t.Helper()
	srv := sseServer(t, body)
	client, err := NewClient(config.Provider{
		Protocol: config.ProtocolOpenAI,
		APIKey:   "test",
		BaseURL:  srv.URL,
		Model:    "test",
	}, nil, "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestOpenAIUsageContract(t *testing.T) {
	// input_tokens is inclusive: 1150 = 1000 cached + 50 written + 100 plain.
	const body = `event: response.completed
data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"test","output":[],"parallel_tool_calls":false,"tool_choice":"auto","tools":[],"usage":{"input_tokens":1150,"input_tokens_details":{"cached_tokens":1000,"cache_write_tokens":50},"output_tokens":200,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":1350}}}

`
	got := streamUsage(t, openAITestClient(t, body))
	if got != contractUsage {
		t.Errorf("usage = %+v, want %+v", got, contractUsage)
	}
}

// completedEvent ends a fixture stream; the response body is the minimum the
// SDK parses.
const completedEvent = `event: response.completed
data: {"type":"response.completed","sequence_number":9,"response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"test","output":[],"parallel_tool_calls":false,"tool_choice":"auto","tools":[],"usage":{"input_tokens":10,"input_tokens_details":{},"output_tokens":5,"output_tokens_details":{},"total_tokens":15}}}

`

// streamThinking collects the reasoning blocks a stream emitted.
func streamThinking(t *testing.T, client Client) []ThinkingComplete {
	t.Helper()
	var blocks []ThinkingComplete
	for ev := range client.Stream(context.Background(), Request{}) {
		switch e := ev.(type) {
		case ThinkingComplete:
			blocks = append(blocks, e)
		case StreamError:
			t.Fatalf("stream error: %v", e.Err)
		}
	}
	return blocks
}

// The completed reasoning item is the authoritative copy: the deltas arrive
// first, but the block must carry the item's own content and encrypted payload
// so the next request replays it verbatim.
func TestOpenAIReasoningCaptureFromItem(t *testing.T) {
	const body = `event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"reasoning","id":"rs_1","status":"in_progress","content":[],"summary":[],"encrypted_content":"enc-added"},"output_index":0,"sequence_number":1}

event: response.reasoning_text.delta
data: {"type":"response.reasoning_text.delta","content_index":0,"delta":"think ","item_id":"rs_1","output_index":0,"sequence_number":2}

event: response.reasoning_text.done
data: {"type":"response.reasoning_text.done","content_index":0,"item_id":"rs_1","output_index":0,"sequence_number":3,"text":"think twice"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","status":"completed","content":[{"type":"reasoning_text","text":"think twice"}],"summary":[{"type":"summary_text","text":"a summary"}],"encrypted_content":"enc-final"},"output_index":0,"sequence_number":4}

` + completedEvent
	blocks := streamThinking(t, openAITestClient(t, body))
	if len(blocks) != 1 {
		t.Fatalf("blocks = %+v, want exactly one", blocks)
	}
	want := ThinkingComplete{Thinking: "a summary", Content: "think twice", Signature: "rs_1", Encrypted: "enc-final"}
	if blocks[0] != want {
		t.Errorf("block = %+v, want %+v", blocks[0], want)
	}
}

// A stream that dies before the completed item still hands over what the
// deltas accumulated rather than dropping the reasoning.
func TestOpenAIReasoningCaptureFallsBackToDeltas(t *testing.T) {
	const body = `event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"reasoning","id":"rs_2","status":"in_progress","content":[],"summary":[]},"output_index":0,"sequence_number":1}

event: response.reasoning_text.delta
data: {"type":"response.reasoning_text.delta","content_index":0,"delta":"half a thought","item_id":"rs_2","output_index":0,"sequence_number":2}

`
	// The fixture stream dies without a completed event, so the StreamError is
	// part of the scenario; only the block matters here.
	var blocks []ThinkingComplete
	for ev := range openAITestClient(t, body).Stream(context.Background(), Request{}) {
		if e, ok := ev.(ThinkingComplete); ok {
			blocks = append(blocks, e)
		}
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %+v, want exactly one", blocks)
	}
	want := ThinkingComplete{Content: "half a thought", Signature: "rs_2"}
	if blocks[0] != want {
		t.Errorf("block = %+v, want %+v", blocks[0], want)
	}
}

// A tool call must be echoed with the provider's call id — that is the name
// the function_call_output references — not the item id.
func TestOpenAIToolCallCarriesCallID(t *testing.T) {
	const body = `event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item_1","status":"in_progress","arguments":"","call_id":"call_abc","name":"Read"},"output_index":0,"sequence_number":1}

event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","item_id":"item_1","output_index":0,"arguments":"{\"file_path\":\"a.go\"}","sequence_number":2}

` + completedEvent
	var calls []ToolCallComplete
	for ev := range openAITestClient(t, body).Stream(context.Background(), Request{}) {
		switch e := ev.(type) {
		case ToolCallComplete:
			calls = append(calls, e)
		case StreamError:
			t.Fatalf("stream error: %v", e.Err)
		}
	}
	if len(calls) != 1 || calls[0].ToolID != "call_abc" || calls[0].ToolName != "Read" {
		t.Fatalf("calls = %+v, want the call_id-tagged Read call", calls)
	}
	if calls[0].Arguments["file_path"] != "a.go" {
		t.Errorf("arguments = %v", calls[0].Arguments)
	}
}

// Replay must put back every captured part; a thinking-mode endpoint rejects a
// replayed tool turn whose reasoning_text is missing.
func TestOpenAIReasoningReplay(t *testing.T) {
	msgs := []conversation.Message{{
		Role: conversation.RoleAssistant,
		ThinkingBlocks: []conversation.ThinkingBlock{
			{Signature: "rs_1", Content: "raw reasoning", Encrypted: "enc"},
		},
	}}
	items := buildOpenAIMessages(msgs)
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 {
		t.Fatalf("items = %s, want a single reasoning item", raw)
	}
	item := decoded[0]
	if item["type"] != "reasoning" || item["id"] != "rs_1" || item["encrypted_content"] != "enc" {
		t.Errorf("item = %v", item)
	}
	content, ok := item["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %v, want the reasoning_text part", item["content"])
	}
	part := content[0].(map[string]any)
	if part["type"] != "reasoning_text" || part["text"] != "raw reasoning" {
		t.Errorf("content part = %v", part)
	}
	if _, present := item["summary"]; present {
		t.Errorf("an empty summary must be omitted: %v", item)
	}
}

func TestMapOpenAIError(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{"authentication hint", "http 401 unauthorized", "auth"},
		{"forbidden hint", "403 forbidden", "auth"},
		{"rate limit hint", "429 too many requests", "rate"},
		{"rate limit phrase", "rate limit reached", "rate"},
		{"plain error", "something odd", "network"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := errorKind(mapOpenAIError(errors.New(tt.message)))
			if got != tt.want {
				t.Errorf("kind = %q, want %q", got, tt.want)
			}
		})
	}
}
