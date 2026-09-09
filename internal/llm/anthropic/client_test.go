package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/llm"
)

const testKey = "SECRET-KEY-123"

type capture struct {
	mu   sync.Mutex
	body []byte
}

func (c *capture) set(b []byte) { c.mu.Lock(); c.body = b; c.mu.Unlock() }
func (c *capture) get() []byte  { c.mu.Lock(); defer c.mu.Unlock(); return c.body }

// newServer replays testdata/<file> as an SSE response and captures the request body.
func newServer(t *testing.T, file string, cap *capture) *httptest.Server {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", file))
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

func newClient(srv *httptest.Server, thinking bool) *Client {
	return New(config.Provider{
		Name: "t", Protocol: config.ProtocolAnthropic, Model: "claude-test",
		BaseURL: srv.URL, APIKey: testKey, Thinking: thinking,
	}, srv.Client())
}

func collect(t *testing.T, ch <-chan llm.StreamEvent) []llm.StreamEvent {
	t.Helper()
	var out []llm.StreamEvent
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

func joinText(evs []llm.StreamEvent) string {
	var b strings.Builder
	for _, e := range evs {
		b.WriteString(e.Text)
	}
	return b.String()
}

func simpleReq(thinking bool) llm.Request {
	return llm.Request{System: "You are AetherCode.", Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}, Thinking: thinking}
}

func assertTextThenDone(t *testing.T, evs []llm.StreamEvent) {
	t.Helper()
	if len(evs) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(evs), evs)
	}
	if evs[0].Type != llm.StreamEventTextDelta || evs[0].Text != "Hello" {
		t.Errorf("evs[0] = %+v", evs[0])
	}
	if evs[1].Type != llm.StreamEventTextDelta || evs[1].Text != ", world" {
		t.Errorf("evs[1] = %+v", evs[1])
	}
	if evs[2].Type != llm.StreamEventDone {
		t.Errorf("evs[2] = %+v, want Done", evs[2])
	}
}

func TestStreamTextOnly(t *testing.T) {
	srv := newServer(t, "text_only.sse", &capture{})
	defer srv.Close()
	evs := collect(t, newClient(srv, false).Stream(context.Background(), simpleReq(false)))
	assertTextThenDone(t, evs)
}

func TestStreamThinkingDropped(t *testing.T) {
	cap := &capture{}
	srv := newServer(t, "thinking_and_text.sse", cap)
	defer srv.Close()
	evs := collect(t, newClient(srv, true).Stream(context.Background(), simpleReq(true)))
	assertTextThenDone(t, evs)
	if strings.Contains(joinText(evs), "Let me think") {
		t.Error("thinking text leaked into events")
	}
	var body map[string]any
	if err := json.Unmarshal(cap.get(), &body); err != nil {
		t.Fatalf("request body not JSON: %v\n%s", err, cap.get())
	}
	th, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking param missing: %s", cap.get())
	}
	if th["type"] != "enabled" || th["budget_tokens"] != float64(8192) {
		t.Errorf("thinking = %v", th)
	}
}

func TestStreamNoThinkingParam(t *testing.T) {
	cap := &capture{}
	srv := newServer(t, "text_only.sse", cap)
	defer srv.Close()
	collect(t, newClient(srv, false).Stream(context.Background(), simpleReq(false)))
	var body map[string]any
	if err := json.Unmarshal(cap.get(), &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["thinking"]; present {
		t.Errorf("thinking key should be absent: %s", cap.get())
	}
}

func TestStreamErrorEvent(t *testing.T) {
	srv := newServer(t, "error_event.sse", &capture{})
	defer srv.Close()
	evs := collect(t, newClient(srv, false).Stream(context.Background(), simpleReq(false)))
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	last := evs[len(evs)-1]
	if last.Type != llm.StreamEventError {
		t.Fatalf("last = %+v, want Error", last)
	}
	if !strings.Contains(last.Err.Error(), "Overloaded") {
		t.Errorf("error text = %q", last.Err)
	}
	for _, e := range evs {
		if e.Type == llm.StreamEventDone {
			t.Error("Done must not be emitted on error")
		}
	}
}

func TestStreamHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()
	evs := collect(t, newClient(srv, false).Stream(context.Background(), simpleReq(false)))
	if len(evs) != 1 || evs[0].Type != llm.StreamEventError {
		t.Fatalf("events = %+v, want single Error", evs)
	}
	msg := evs[0].Err.Error()
	if !strings.Contains(msg, "401") && !strings.Contains(msg, "invalid x-api-key") {
		t.Errorf("error should mention status or message: %q", msg)
	}
	if strings.Contains(msg, testKey) {
		t.Errorf("api key leaked: %q", msg)
	}
}

func TestStreamRequestBody(t *testing.T) {
	cap := &capture{}
	srv := newServer(t, "text_only.sse", cap)
	defer srv.Close()
	req := llm.Request{
		System: "You are AetherCode.",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "q1"},
			{Role: llm.RoleAssistant, Content: "a1"},
			{Role: llm.RoleUser, Content: "q2"},
		},
	}
	collect(t, newClient(srv, false).Stream(context.Background(), req))
	var body struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Stream    bool   `json:"stream"`
		System    []struct {
			Text string `json:"text"`
		} `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(cap.get(), &body); err != nil {
		t.Fatalf("%v\n%s", err, cap.get())
	}
	if body.Model != "claude-test" || body.MaxTokens != 16384 || !body.Stream {
		t.Errorf("model/max_tokens/stream = %q/%d/%v", body.Model, body.MaxTokens, body.Stream)
	}
	if len(body.System) != 1 || body.System[0].Text != "You are AetherCode." {
		t.Errorf("system = %+v", body.System)
	}
	if len(body.Messages) != 3 {
		t.Fatalf("messages len = %d", len(body.Messages))
	}
	wantRoles := []string{"user", "assistant", "user"}
	wantText := []string{"q1", "a1", "q2"}
	for i, m := range body.Messages {
		if m.Role != wantRoles[i] || len(m.Content) != 1 || m.Content[0].Text != wantText[i] {
			t.Errorf("messages[%d] = %+v", i, m)
		}
	}
}

func TestStreamCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"x\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	ch := newClient(srv, false).Stream(ctx, simpleReq(false))
	time.Sleep(500 * time.Millisecond)
	cancel()
	start := time.Now()
	evs := collect(t, ch)
	if time.Since(start) > 2*time.Second {
		t.Errorf("channel took too long to close: %v", time.Since(start))
	}
	if len(evs) == 0 {
		t.Fatal("expected an Error event")
	}
	last := evs[len(evs)-1]
	if last.Type != llm.StreamEventError || !errors.Is(last.Err, context.Canceled) {
		t.Errorf("last = %+v, want Error(context.Canceled)", last)
	}
}
