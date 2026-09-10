package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
)

func newAnthropicTestClient(srv *httptest.Server) Client {
	return newAnthropicClient(config.Provider{
		Name: "t", Protocol: config.ProtocolAnthropic, Model: "claude-test",
		BaseURL: srv.URL, APIKey: testKey,
	}, srv.Client())
}

func TestAnthropicStreamTextOnly(t *testing.T) {
	srv := newServer(t, "anthropic", "text_only.sse", &capture{})
	defer srv.Close()
	assertTextThenDone(t, collect(t, newAnthropicTestClient(srv).Stream(context.Background(), simpleReq(false))))
}

func TestAnthropicStreamEndUsage(t *testing.T) {
	srv := newServer(t, "anthropic", "text_only.sse", &capture{})
	defer srv.Close()
	evs := collect(t, newAnthropicTestClient(srv).Stream(context.Background(), simpleReq(false)))
	last := evs[len(evs)-1]
	end, ok := last.(StreamEnd)
	if !ok {
		t.Fatalf("last = %T, want StreamEnd", last)
	}
	if end.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", end.StopReason)
	}
	if end.Usage.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, want 5", end.Usage.OutputTokens)
	}
}

func TestAnthropicStreamThinkingForwarded(t *testing.T) {
	cap := &capture{}
	srv := newServer(t, "anthropic", "thinking_and_text.sse", cap)
	defer srv.Close()
	evs := collect(t, newAnthropicTestClient(srv).Stream(context.Background(), simpleReq(true)))

	// Thinking block: one delta, one complete; then the text block.
	var td []ThinkingDelta
	var tc []ThinkingComplete
	for _, e := range evs {
		switch v := e.(type) {
		case ThinkingDelta:
			td = append(td, v)
		case ThinkingComplete:
			tc = append(tc, v)
		}
	}
	if len(td) != 1 || td[0].Text != "Let me think" {
		t.Errorf("thinking deltas = %+v", td)
	}
	if len(tc) != 1 || tc[0].Thinking != "Let me think" || tc[0].Signature != "EqQBCgIYAhIM" {
		t.Errorf("thinking complete = %+v", tc)
	}
	assertTextThenDone(t, evs)

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

func TestAnthropicStreamNoThinkingParam(t *testing.T) {
	cap := &capture{}
	srv := newServer(t, "anthropic", "text_only.sse", cap)
	defer srv.Close()
	collect(t, newAnthropicTestClient(srv).Stream(context.Background(), simpleReq(false)))
	var body map[string]any
	if err := json.Unmarshal(cap.get(), &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["thinking"]; present {
		t.Errorf("thinking key should be absent: %s", cap.get())
	}
}

func TestAnthropicStreamErrorEvent(t *testing.T) {
	srv := newServer(t, "anthropic", "error_event.sse", &capture{})
	defer srv.Close()
	evs := collect(t, newAnthropicTestClient(srv).Stream(context.Background(), simpleReq(false)))
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	last, ok := lastError(evs)
	if !ok {
		t.Fatalf("last = %+v, want StreamError", evs[len(evs)-1])
	}
	if !strings.Contains(last.Err.Error(), "Overloaded") {
		t.Errorf("error text = %q", last.Err)
	}
	assertNoDoneOnError(t, evs)
	var se *ServerError
	if !errors.As(last.Err, &se) {
		t.Errorf("want ServerError, got %T: %v", last.Err, last.Err)
	}
}

func TestAnthropicStreamHTTPError_Classified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()
	evs := collect(t, newAnthropicTestClient(srv).Stream(context.Background(), simpleReq(false)))
	if len(evs) != 1 {
		t.Fatalf("events = %+v, want single StreamError", evs)
	}
	se, ok := lastError(evs)
	if !ok {
		t.Fatalf("want StreamError, got %+v", evs)
	}
	var ae *AuthenticationError
	if !errors.As(se.Err, &ae) {
		t.Errorf("want AuthenticationError, got %T: %v", se.Err, se.Err)
	}
	if strings.Contains(se.Err.Error(), testKey) {
		t.Errorf("api key leaked: %q", se.Err)
	}
}

func TestAnthropicStreamRequestBody(t *testing.T) {
	cap := &capture{}
	srv := newServer(t, "anthropic", "text_only.sse", cap)
	defer srv.Close()
	req := Request{
		System: "You are AetherCode.",
		Messages: []conversation.Message{
			{Role: conversation.RoleUser, Content: "q1"},
			{Role: conversation.RoleAssistant, Content: "a1"},
			{Role: conversation.RoleUser, Content: "q2"},
		},
	}
	collect(t, newAnthropicTestClient(srv).Stream(context.Background(), req))
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

func TestAnthropicMergesConsecutiveSameRole(t *testing.T) {
	cap := &capture{}
	srv := newServer(t, "anthropic", "text_only.sse", cap)
	defer srv.Close()
	req := Request{
		System: "sys",
		Messages: []conversation.Message{
			{Role: conversation.RoleUser, Content: "a"},
			{Role: conversation.RoleUser, Content: "b"},
			{Role: conversation.RoleAssistant, Content: "c"},
			{Role: conversation.RoleAssistant, Content: "d"},
		},
	}
	collect(t, newAnthropicTestClient(srv).Stream(context.Background(), req))
	var body struct {
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
	if len(body.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2 (merged): %+v", len(body.Messages), body.Messages)
	}
	if body.Messages[0].Role != "user" || len(body.Messages[0].Content) != 1 || body.Messages[0].Content[0].Text != "a\n\nb" {
		t.Errorf("messages[0] = %+v", body.Messages[0])
	}
	if body.Messages[1].Role != "assistant" || body.Messages[1].Content[0].Text != "c\n\nd" {
		t.Errorf("messages[1] = %+v", body.Messages[1])
	}
}

func TestAnthropicStreamCancel(t *testing.T) {
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
	ch := newAnthropicTestClient(srv).Stream(ctx, simpleReq(false))
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
	se, ok := lastError(evs)
	if !ok || !errors.Is(se.Err, context.Canceled) {
		t.Errorf("last = %+v, want StreamError(context.Canceled)", evs[len(evs)-1])
	}
}
