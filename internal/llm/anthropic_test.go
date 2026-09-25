package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
)

func anthropicTestClient(t *testing.T, body string) Client {
	t.Helper()
	srv := sseServer(t, body)
	client, err := NewClient(config.Provider{
		Protocol: config.ProtocolAnthropic,
		APIKey:   "test",
		BaseURL:  srv.URL,
		Model:    "test",
	}, nil, "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// The wire usage in every fixture below normalizes to contractUsage.
func TestAnthropicUsageContract(t *testing.T) {
	// message_start carries the input side; message_delta carries only the
	// cumulative output.
	const body = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":1000,"cache_creation_input_tokens":50}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":200}}

event: message_stop
data: {"type":"message_stop"}

`
	got := streamUsage(t, anthropicTestClient(t, body))
	if got != contractUsage {
		t.Errorf("usage = %+v, want %+v", got, contractUsage)
	}
}

func TestAnthropicUsageFromDeltaOnly(t *testing.T) {
	// A gateway that seeds nothing in message_start and reports the whole
	// accounting in message_delta.
	const body = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":100,"output_tokens":200,"cache_read_input_tokens":1000,"cache_creation_input_tokens":50}}

event: message_stop
data: {"type":"message_stop"}

`
	got := streamUsage(t, anthropicTestClient(t, body))
	if got != contractUsage {
		t.Errorf("usage = %+v, want %+v", got, contractUsage)
	}
}

// A redacted thinking block has no deltas: the opaque data is the payload, and
// it must be captured for the next request to hand back unchanged.
func TestAnthropicRedactedThinkingRoundTrip(t *testing.T) {
	const body = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"opaque-blob"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`
	var blocks []ThinkingComplete
	for ev := range anthropicTestClient(t, body).Stream(context.Background(), Request{}) {
		switch e := ev.(type) {
		case ThinkingComplete:
			blocks = append(blocks, e)
		case StreamError:
			t.Fatalf("stream error: %v", e.Err)
		}
	}
	want := ThinkingComplete{Encrypted: "opaque-blob"}
	if len(blocks) != 1 || blocks[0] != want {
		t.Fatalf("blocks = %+v, want one %+v", blocks, want)
	}

	msgs := []conversation.Message{{
		Role:           conversation.RoleAssistant,
		ThinkingBlocks: []conversation.ThinkingBlock{{Encrypted: "opaque-blob"}},
	}}
	raw, err := json.Marshal(buildAnthropicMessages(msgs))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"type":"redacted_thinking"`) || !strings.Contains(string(raw), `"data":"opaque-blob"`) {
		t.Errorf("replay = %s, want the redacted block back", raw)
	}
}

func TestMapAnthropicError(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{"authentication hint", "http 401 unauthorized", "auth"},
		{"forbidden hint", "403 forbidden", "auth"},
		{"rate limit hint", "429 too many requests", "rate"},
		{"rate limit phrase", "rate limit exceeded", "rate"},
		{"overloaded hint", "overloaded_error: server busy", "server"},
		{"overload status", "server error 529", "server"},
		{"plain error", "something odd", "network"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := errorKind(mapAnthropicError(errors.New(tt.message)))
			if got != tt.want {
				t.Errorf("kind = %q, want %q", got, tt.want)
			}
		})
	}
}
