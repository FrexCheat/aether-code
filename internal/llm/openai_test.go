package llm

import (
	"errors"
	"testing"

	"github.com/FrexCheat/aether-code/internal/config"
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
