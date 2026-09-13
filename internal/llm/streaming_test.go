package llm

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/FrexCheat/aether-code/internal/conversation"
)

const testKey = "SECRET-KEY-123"

type capture struct {
	mu   sync.Mutex
	body []byte
}

func (c *capture) set(b []byte) { c.mu.Lock(); c.body = b; c.mu.Unlock() }
func (c *capture) get() []byte  { c.mu.Lock(); defer c.mu.Unlock(); return c.body }

// newServer replays testdata/<provider>/<file> as an SSE response and captures
// the request body.
func newServer(t *testing.T, provider, file string, cap *capture) *httptest.Server {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", provider, file))
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.set(b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
}

func collect(t *testing.T, ch <-chan StreamEvent) []StreamEvent {
	t.Helper()
	var out []StreamEvent
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatalf("timed out waiting for stream to close; got %d events", len(out))
		}
	}
}

func simpleReq(thinking bool) Request {
	return Request{
		System:   "You are AetherCode.",
		Messages: []conversation.Message{{Role: conversation.RoleUser, Content: "hi"}},
		Thinking: thinking,
	}
}

func assertTextThenDone(t *testing.T, evs []StreamEvent) {
	t.Helper()
	var texts []string
	var sawEnd bool
	for _, e := range evs {
		switch v := e.(type) {
		case TextDelta:
			texts = append(texts, v.Text)
		case StreamEnd:
			sawEnd = true
			_ = v
		case ThinkingDelta, ThinkingComplete:
			// thinking fixtures also flow through here; only text matters.
		case ToolCallComplete:
			t.Fatalf("unexpected tool event in text stream: %+v", e)
		case StreamError:
			t.Fatalf("unexpected StreamError: %v", v.Err)
		}
	}
	if len(texts) != 2 || texts[0] != "Hello" || texts[1] != ", world" {
		t.Fatalf("text deltas = %v, want [Hello , world]", texts)
	}
	if !sawEnd {
		t.Fatalf("want StreamEnd, got %v", evs)
	}
}

func assertNoDoneOnError(t *testing.T, evs []StreamEvent) {
	t.Helper()
	for _, e := range evs {
		if _, ok := e.(StreamEnd); ok {
			t.Error("StreamEnd must not be emitted on error")
		}
	}
}

func lastError(evs []StreamEvent) (StreamError, bool) {
	if len(evs) == 0 {
		return StreamError{}, false
	}
	se, ok := evs[len(evs)-1].(StreamError)
	return se, ok
}
