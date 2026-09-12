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
	"github.com/FrexCheat/aether-code/internal/tools"
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

func TestOpenAIRequestBodyWithTools(t *testing.T) {
	cap := &capture{}
	srv := newServer(t, "openai", "text_only.sse", cap)
	defer srv.Close()
	req := Request{
		System: "sys",
		Tools: []map[string]any{
			{
				"name":        "read_file",
				"description": "Read a file from disk",
				"input_schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_path": map[string]any{"type": "string"},
					},
					"required": []string{"file_path"},
				},
			},
		},
		Messages: []conversation.Message{
			{Role: conversation.RoleUser, Content: "q"},
			{Role: conversation.RoleAssistant, Blocks: []conversation.Block{
				conversation.TextBlock{Text: "checking"},
				conversation.ToolUseBlock{ID: "fc_1", Name: "read_file", Args: map[string]any{"file_path": "a.go"}},
			}},
			{Role: conversation.RoleUser, Blocks: []conversation.Block{
				conversation.ToolResultBlock{ToolUseID: "fc_1", Content: "file body"},
			}},
		},
	}
	collect(t, newOpenAITestClient(srv).Stream(context.Background(), req))

	var body struct {
		Tools []struct {
			Type        string `json:"type"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Parameters  map[string]any
			Strict      bool `json:"strict"`
		} `json:"tools"`
		Input []struct {
			Type      string `json:"type"`
			Role      string `json:"role"`
			Content   string `json:"content"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Output    string `json:"output"`
		} `json:"input"`
	}
	if err := json.Unmarshal(cap.get(), &body); err != nil {
		t.Fatalf("%v\n%s", err, cap.get())
	}
	if len(body.Tools) != 1 {
		t.Fatalf("tools = %+v, want 1", body.Tools)
	}
	tool := body.Tools[0]
	if tool.Type != "function" || tool.Name != "read_file" || tool.Description != "Read a file from disk" {
		t.Errorf("tool = %+v", tool)
	}
	if tool.Strict {
		t.Errorf("strict should be false: %+v", tool)
	}
	if _, ok := tool.Parameters["properties"]; !ok {
		t.Errorf("parameters missing properties: %+v", tool.Parameters)
	}
	// Input: user message, assistant text (separate message item per block),
	// function_call item, function_call_output item.
	if len(body.Input) != 4 {
		t.Fatalf("input len = %d: %s", len(body.Input), cap.get())
	}
	if body.Input[0].Role != "user" || body.Input[0].Content != "q" {
		t.Errorf("input[0] = %+v", body.Input[0])
	}
	if body.Input[1].Role != "assistant" || body.Input[1].Content != "checking" {
		t.Errorf("input[1] = %+v", body.Input[1])
	}
	fc := body.Input[2]
	if fc.Type != "function_call" || fc.CallID != "fc_1" || fc.Name != "read_file" {
		t.Errorf("function_call item = %+v", fc)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(fc.Arguments), &args); err != nil || args["file_path"] != "a.go" {
		t.Errorf("arguments = %q (err=%v)", fc.Arguments, err)
	}
	out := body.Input[3]
	if out.Type != "function_call_output" || out.CallID != "fc_1" || out.Output != "file body" {
		t.Errorf("function_call_output item = %+v", out)
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

// TestOpenAIRegistrySchemasSurviveTranslation runs the real pipeline —
// tools.Registry.GetAllSchemas("openai") feeding buildParams — to prove the
// registry's "parameters" key is consumed. A key-name mismatch here used to
// send parameters:{} on the wire and got every request rejected with 400
// "Invalid schema for function ...".
func TestOpenAIRegistrySchemasSurviveTranslation(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&tools.ReadFileTool{WorkDir: t.TempDir()})
	cap := &capture{}
	srv := newServer(t, "openai", "text_only.sse", cap)
	defer srv.Close()
	c := newOpenAITestClient(srv).(*openAIClient)
	req := simpleReq(false)
	req.Tools = reg.GetAllSchemas("openai")
	collect(t, c.Stream(context.Background(), req))

	var body struct {
		Tools []struct {
			Name       string         `json:"name"`
			Parameters map[string]any `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(cap.get(), &body); err != nil {
		t.Fatalf("%v\n%s", err, cap.get())
	}
	if len(body.Tools) != 1 {
		t.Fatalf("tools = %+v, want 1", body.Tools)
	}
	if body.Tools[0].Name != "read_file" {
		t.Errorf("name = %q", body.Tools[0].Name)
	}
	if body.Tools[0].Parameters["type"] != "object" {
		t.Errorf("parameters = %+v, want a type:object schema, not {}", body.Tools[0].Parameters)
	}
	props, _ := body.Tools[0].Parameters["properties"].(map[string]any)
	if _, ok := props["file_path"]; !ok {
		t.Errorf("parameters.properties missing file_path: %+v", body.Tools[0].Parameters)
	}
}
