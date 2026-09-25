package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// contractUsage is the normalized result every adapter's StreamEnd must
// produce from its protocol's wire usage. Anthropic reports the input side as
// three disjoint fields; OpenAI reports it inclusively — both must fold into
// this one shape.
var contractUsage = Usage{
	InputTokens:         100,
	OutputTokens:        200,
	CacheReadTokens:     1000,
	CacheCreationTokens: 50,
}

// sseServer answers every request with a fixed event-stream body.
func sseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// streamUsage runs one request to completion and returns the usage it ended on.
func streamUsage(t *testing.T, client Client) Usage {
	t.Helper()
	var usage Usage
	sawEnd := false
	for ev := range client.Stream(context.Background(), Request{}) {
		switch e := ev.(type) {
		case StreamEnd:
			usage, sawEnd = e.Usage, true
		case StreamError:
			t.Fatalf("stream error: %v", e.Err)
		}
	}
	if !sawEnd {
		t.Fatal("stream ended without StreamEnd")
	}
	return usage
}

func TestUsageAccessors(t *testing.T) {
	tests := []struct {
		name   string
		usage  Usage
		prompt int
		total  int
	}{
		{"zero", Usage{}, 0, 0},
		{"cache only", Usage{CacheReadTokens: 100, CacheCreationTokens: 50}, 150, 150},
		{"full", Usage{InputTokens: 10, OutputTokens: 3, CacheReadTokens: 100, CacheCreationTokens: 50}, 160, 163},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.usage.PromptTokens(); got != tt.prompt {
				t.Errorf("PromptTokens() = %d, want %d", got, tt.prompt)
			}
			if got := tt.usage.TotalTokens(); got != tt.total {
				t.Errorf("TotalTokens() = %d, want %d", got, tt.total)
			}
		})
	}
}

func TestEndStream(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	background := context.Background()
	cause := errors.New("cause")

	tests := []struct {
		name      string
		ctx       context.Context
		streamErr error
		failErr   error
		completed bool
		wantEnd   bool
		wantErr   error  // compared with errors.Is; nil means "unexpected" message expected
		wantMsg   string // checked when wantErr is nil and wantEnd is false
	}{
		{"cancellation wins", cancelled, cause, cause, true, false, context.Canceled, ""},
		{"stream error beats in-band", background, cause, errors.New("in-band"), true, false, cause, ""},
		{"in-band failure", background, nil, cause, true, false, cause, ""},
		{"completed", background, nil, nil, true, true, nil, ""},
		{"unexpected", background, nil, nil, false, false, nil, "stream ended unexpectedly"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := endStream(tt.ctx, tt.streamErr, tt.failErr, tt.completed, contractUsage)
			if tt.wantEnd {
				end, ok := ev.(StreamEnd)
				if !ok {
					t.Fatalf("event = %T, want StreamEnd", ev)
				}
				if end.Usage != contractUsage {
					t.Errorf("usage = %+v, want %+v", end.Usage, contractUsage)
				}
				return
			}
			streamErr, ok := ev.(StreamError)
			if !ok {
				t.Fatalf("event = %T, want StreamError", ev)
			}
			if tt.wantErr != nil && !errors.Is(streamErr.Err, tt.wantErr) {
				t.Errorf("err = %v, want %v", streamErr.Err, tt.wantErr)
			}
			if tt.wantMsg != "" && streamErr.Err.Error() != tt.wantMsg {
				t.Errorf("err = %q, want %q", streamErr.Err.Error(), tt.wantMsg)
			}
		})
	}
}

func TestMergeUsage(t *testing.T) {
	tests := []struct {
		name  string
		total Usage
		next  Usage
		want  Usage
	}{
		{
			// The shape a strict gateway produces: input side only in the
			// start event, cumulative output only in the delta.
			name:  "delta carries output only",
			total: Usage{InputTokens: 100, OutputTokens: 1, CacheReadTokens: 1000, CacheCreationTokens: 50},
			next:  Usage{OutputTokens: 200},
			want:  contractUsage,
		},
		{
			// The other gateway shape: everything in the delta, nothing in
			// the start event.
			name:  "start carries nothing",
			total: Usage{},
			next:  Usage{InputTokens: 100, OutputTokens: 200, CacheReadTokens: 1000, CacheCreationTokens: 50},
			want:  contractUsage,
		},
		{
			name:  "later duplicate never lowers a field",
			total: Usage{InputTokens: 100, OutputTokens: 200},
			next:  Usage{InputTokens: 100, OutputTokens: 0},
			want:  Usage{InputTokens: 100, OutputTokens: 200},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mergeUsage(tt.total, tt.next); got != tt.want {
				t.Errorf("mergeUsage(%+v, %+v) = %+v, want %+v", tt.total, tt.next, got, tt.want)
			}
		})
	}
}
