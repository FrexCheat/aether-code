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

func newOpenAITestClient(srv *httptest.Server) Client {
	return newOpenAIClient(config.Provider{
		Name: "t", Protocol: config.ProtocolOpenAI, Model: "gpt-test",
		BaseURL: srv.URL, APIKey: testKey,
	}, srv.Client())
}

func TestOpenAIStreamTextOnly(t *testing.T) {
	srv := newServer(t, "openai", "text_only.sse", &capture{})
	defer srv.Close()
	assertTextThenDone(t, collect(t, newOpenAITestClient(srv).Stream(context.Background(), simpleReq(false))))
}

func TestOpenAIStreamEndUsage(t *testing.T) {
	srv := newServer(t, "openai", "text_only_usage.sse", &capture{})
	defer srv.Close()
	evs := collect(t, newOpenAITestClient(srv).Stream(context.Background(), simpleReq(false)))
	last := evs[len(evs)-1]
	end, ok := last.(StreamEnd)
	if !ok {
		t.Fatalf("last = %T, want StreamEnd", last)
	}
	if end.StopReason != "completed" {
		t.Errorf("StopReason = %q, want completed", end.StopReason)
	}
	if end.Usage.InputTokens != 12 || end.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v", end.Usage)
	}
	if end.Usage.CacheReadTokens != 8 {
		t.Errorf("CacheReadTokens = %d, want 8", end.Usage.CacheReadTokens)
	}
}

func TestOpenAIStreamReasoningForwarded(t *testing.T) {
	cap := &capture{}
	srv := newServer(t, "openai", "reasoning_and_text.sse", cap)
	defer srv.Close()
	evs := collect(t, newOpenAITestClient(srv).Stream(context.Background(), simpleReq(true)))

	var td []ThinkingDelta
	for _, e := range evs {
		if v, ok := e.(ThinkingDelta); ok {
			td = append(td, v)
		}
	}
	if len(td) != 1 || td[0].Text != "Thinking about it" {
		t.Errorf("thinking deltas = %+v", td)
	}
	assertTextThenDone(t, evs)

	var body map[string]any
	if err := json.Unmarshal(cap.get(), &body); err != nil {
		t.Fatalf("%v\n%s", err, cap.get())
	}
	r, ok := body["reasoning"].(map[string]any)
	if !ok || r["effort"] != "medium" {
		t.Errorf("reasoning = %v (body %s)", body["reasoning"], cap.get())
	}
}

func TestOpenAIStreamNoReasoningParam(t *testing.T) {
	cap := &capture{}
	srv := newServer(t, "openai", "text_only.sse", cap)
	defer srv.Close()
	collect(t, newOpenAITestClient(srv).Stream(context.Background(), simpleReq(false)))
	var body map[string]any
	if err := json.Unmarshal(cap.get(), &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["reasoning"]; present {
		t.Errorf("reasoning key should be absent: %s", cap.get())
	}
}

func TestOpenAIStreamToolCall(t *testing.T) {
	srv := newServer(t, "openai", "tool_call.sse", &capture{})
	defer srv.Close()
	evs := collect(t, newOpenAITestClient(srv).Stream(context.Background(), simpleReq(false)))

	var start ToolCallStart
	var deltas []ToolCallDelta
	var complete ToolCallComplete
	for _, e := range evs {
		switch v := e.(type) {
		case ToolCallStart:
			start = v
		case ToolCallDelta:
			deltas = append(deltas, v)
		case ToolCallComplete:
			complete = v
		}
	}
	if start.ToolName != "get_weather" || start.ToolID != "call_1" {
		t.Errorf("start = %+v", start)
	}
	if len(deltas) != 1 || deltas[0].Text != `{"city":"Paris"}` {
		t.Errorf("deltas = %+v", deltas)
	}
	if complete.ToolID != "call_1" || complete.ToolName != "get_weather" || complete.Arguments["city"] != "Paris" {
		t.Errorf("complete = %+v", complete)
	}
	assertNoDoneOnError(t, nil) // smoke: helper still compiles
	var sawEnd bool
	for _, e := range evs {
		if _, ok := e.(StreamEnd); ok {
			sawEnd = true
		}
	}
	if !sawEnd {
		t.Error("want StreamEnd after tool call")
	}
}

func TestOpenAIStreamFailed_Classified(t *testing.T) {
	srv := newServer(t, "openai", "failed.sse", &capture{})
	defer srv.Close()
	evs := collect(t, newOpenAITestClient(srv).Stream(context.Background(), simpleReq(false)))
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	last, ok := lastError(evs)
	if !ok {
		t.Fatalf("last = %+v, want StreamError", evs[len(evs)-1])
	}
	if !strings.Contains(strings.ToLower(last.Err.Error()), "overloaded") {
		t.Errorf("error text = %q", last.Err)
	}
	assertNoDoneOnError(t, evs)
	var se *ServerError
	if !errors.As(last.Err, &se) {
		t.Errorf("want ServerError, got %T: %v", last.Err, last.Err)
	}
}

func TestOpenAIStreamHTTPError_Classified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`))
	}))
	defer srv.Close()
	evs := collect(t, newOpenAITestClient(srv).Stream(context.Background(), simpleReq(false)))
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

func TestOpenAIStreamRequestBody(t *testing.T) {
	cap := &capture{}
	srv := newServer(t, "openai", "text_only.sse", cap)
	defer srv.Close()
	req := Request{
		System: "You are AetherCode.",
		Messages: []conversation.Message{
			{Role: conversation.RoleUser, Content: "q1"},
			{Role: conversation.RoleAssistant, Content: "a1"},
			{Role: conversation.RoleUser, Content: "q2"},
		},
	}
	collect(t, newOpenAITestClient(srv).Stream(context.Background(), req))
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

func TestOpenAIStreamCancel(t *testing.T) {
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
	ch := newOpenAITestClient(srv).Stream(ctx, simpleReq(false))
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
