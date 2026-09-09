package openai

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
		Name: "t", Protocol: config.ProtocolOpenAI, Model: "gpt-test",
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
	assertTextThenDone(t, collect(t, newClient(srv, false).Stream(context.Background(), simpleReq(false))))
}

func TestStreamReasoningDropped(t *testing.T) {
	cap := &capture{}
	srv := newServer(t, "reasoning_and_text.sse", cap)
	defer srv.Close()
	evs := collect(t, newClient(srv, true).Stream(context.Background(), simpleReq(true)))
	assertTextThenDone(t, evs)
	if strings.Contains(joinText(evs), "Thinking about it") {
		t.Error("reasoning text leaked into events")
	}
	var body map[string]any
	if err := json.Unmarshal(cap.get(), &body); err != nil {
		t.Fatalf("%v\n%s", err, cap.get())
	}
	r, ok := body["reasoning"].(map[string]any)
	if !ok || r["effort"] != "medium" {
		t.Errorf("reasoning = %v (body %s)", body["reasoning"], cap.get())
	}
}

func TestStreamNoReasoningParam(t *testing.T) {
	cap := &capture{}
	srv := newServer(t, "text_only.sse", cap)
	defer srv.Close()
	collect(t, newClient(srv, false).Stream(context.Background(), simpleReq(false)))
	var body map[string]any
	if err := json.Unmarshal(cap.get(), &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["reasoning"]; present {
		t.Errorf("reasoning key should be absent: %s", cap.get())
	}
}

func TestStreamFailed(t *testing.T) {
	srv := newServer(t, "failed.sse", &capture{})
	defer srv.Close()
	evs := collect(t, newClient(srv, false).Stream(context.Background(), simpleReq(false)))
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	last := evs[len(evs)-1]
	if last.Type != llm.StreamEventError || !strings.Contains(strings.ToLower(last.Err.Error()), "overloaded") {
		t.Errorf("last = %+v", last)
	}
	for _, e := range evs {
		if e.Type == llm.StreamEventDone {
			t.Error("Done must not be emitted on failure")
		}
	}
}

func TestStreamHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`))
	}))
	defer srv.Close()
	evs := collect(t, newClient(srv, false).Stream(context.Background(), simpleReq(false)))
	if len(evs) != 1 || evs[0].Type != llm.StreamEventError {
		t.Fatalf("events = %+v, want single Error", evs)
	}
	msg := evs[0].Err.Error()
	if !strings.Contains(msg, "401") && !strings.Contains(msg, "Incorrect API key") {
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
		Model        string `json:"model"`
		Instructions string `json:"instructions"`
		Stream       bool   `json:"stream"`
		Input        []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(cap.get(), &body); err != nil {
		t.Fatalf("%v\n%s", err, cap.get())
	}
	if body.Model != "gpt-test" || body.Instructions != "You are AetherCode." || !body.Stream {
		t.Errorf("model/instructions/stream = %q/%q/%v", body.Model, body.Instructions, body.Stream)
	}
	if len(body.Input) != 3 {
		t.Fatalf("input len = %d: %s", len(body.Input), cap.get())
	}
	wantRoles := []string{"user", "assistant", "user"}
	wantText := []string{"q1", "a1", "q2"}
	for i, m := range body.Input {
		if m.Role != wantRoles[i] || m.Content != wantText[i] {
			t.Errorf("input[%d] = %+v", i, m)
		}
	}
}

func TestStreamCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n"))
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
