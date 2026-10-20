package compaction

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
)

func TestComputeThresholds(t *testing.T) {
	tests := []struct {
		name           string
		window, output int
		wantAuto       int
		wantForce      int
	}{
		// reserve = min(20000, output); usable = window - reserve.
		{"anthropic default", 200000, 16384, 183616 - 13000, 183616 - 3000},
		{"reserve cap applies", 200000, 0, 167000, 177000},
		{"output above cap clamps", 200000, 50000, 167000, 177000},
		{"openai default", 128000, 32000, 95000, 105000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeThresholds(tt.window, tt.output)
			if got.auto != tt.wantAuto || got.force != tt.wantForce {
				t.Errorf("computeThresholds(%d, %d) = %+v, want auto %d force %d",
					tt.window, tt.output, got, tt.wantAuto, tt.wantForce)
			}
		})
	}
}

func TestLevel(t *testing.T) {
	m := NewManager(ManagerParams{ContextWindow: 200000, MaxOutput: 16384})
	// auto = 170616, warn = 150616, force = 180616.
	tests := []struct {
		used int
		want Level
	}{
		{100000, LevelOK},
		{150615, LevelOK},
		{150616, LevelWarn},
		{170616, LevelWarn},
		{180615, LevelWarn},
		{180616, LevelForce},
	}
	for _, tt := range tests {
		m.used.Store(int64(tt.used))
		if got := m.Level(); got != tt.want {
			t.Errorf("Level() at %d = %v, want %v", tt.used, got, tt.want)
		}
	}
}

// stubClient answers every summarize call with the configured outcome and
// counts the calls, so the breaker tests can assert how many doomed LLM calls
// were actually placed.
type stubClient struct {
	fail  bool
	calls int
}

func (s *stubClient) Stream(ctx context.Context, req llm.Request) <-chan llm.StreamEvent {
	s.calls++
	ch := make(chan llm.StreamEvent, 2)
	if s.fail {
		ch <- llm.StreamError{Err: errors.New("summarizer down")}
	} else {
		ch <- llm.TextDelta{Text: "<summary>done</summary>"}
		ch <- llm.StreamEnd{}
	}
	close(ch)
	return ch
}

// breakerManager builds a manager around a fixed-size history: window 20000
// with a 4000 output cap gives usable 16000, auto 3000, force 13000. A
// message of n chars estimates at n/4 + 4 tokens, so six 8000-char messages
// (~12k tokens) sit in the auto band, six 10000-char ones (~15k) in the
// force band, and the keep boundary still leaves an earlier message to
// summarize.
func breakerManager(t *testing.T, messages, fillerChars int) (*Manager, *stubClient) {
	t.Helper()
	conv := conversation.NewManager()
	for i := range messages {
		if err := conv.AddUserMessage(strings.Repeat("a", fillerChars) + string(rune('A'+i))); err != nil {
			t.Fatal(err)
		}
	}
	client := &stubClient{fail: true}
	m := NewManager(ManagerParams{
		Client:        client,
		Conv:          conv,
		Schemas:       stubSchemas(nil),
		ContextWindow: 20000,
		MaxOutput:     4000,
	})
	return m, client
}

func TestPrepareAutoBandBreaker(t *testing.T) {
	m, client := breakerManager(t, 6, 8000)

	for range summaryFailureLimit {
		m.Prepare(context.Background(), nil)
	}
	if client.calls != summaryFailureLimit || m.failures != summaryFailureLimit || m.forceFailures != 0 {
		t.Fatalf("after %d failures: calls = %d, failures = %d, forceFailures = %d",
			summaryFailureLimit, client.calls, m.failures, m.forceFailures)
	}
	// The next round must not place another doomed call.
	m.Prepare(context.Background(), nil)
	if client.calls != summaryFailureLimit {
		t.Errorf("summarize calls = %d after the breaker closed, want no new calls", client.calls)
	}
	// ForceCompact bypasses the check but a failure still feeds the force band.
	if _, err := m.ForceCompact(context.Background(), nil); err == nil {
		t.Fatalf("ForceCompact with a failing summarizer must return the error")
	}
	if client.calls != summaryFailureLimit+1 || m.forceFailures != 1 {
		t.Errorf("ForceCompact must place its call and count it: calls = %d, forceFailures = %d",
			client.calls, m.forceFailures)
	}

	// A successful compaction clears both counters.
	client.fail = false
	if _, err := m.ForceCompact(context.Background(), nil); err != nil {
		t.Fatalf("ForceCompact with a working summarizer: %v", err)
	}
	if m.failures != 0 || m.forceFailures != 0 {
		t.Errorf("success must clear both counters: failures = %d, forceFailures = %d", m.failures, m.forceFailures)
	}
}

func TestPrepareForceBandBreaker(t *testing.T) {
	// Six 10000-char messages ≈ 15k tokens: above the force line 13000.
	m, client := breakerManager(t, 6, 10000)

	for range summaryFailureLimit {
		m.Prepare(context.Background(), nil)
	}
	if client.calls != summaryFailureLimit || m.forceFailures != summaryFailureLimit {
		t.Fatalf("after %d force-band failures: calls = %d, forceFailures = %d",
			summaryFailureLimit, client.calls, m.forceFailures)
	}
	m.Prepare(context.Background(), nil)
	if client.calls != summaryFailureLimit {
		t.Errorf("force band must close after %d failures: calls = %d", summaryFailureLimit, client.calls)
	}
}

func TestPrepareBelowLinesIsNoop(t *testing.T) {
	m, client := breakerManager(t, 2, 4000) // ~2k tokens, below both lines
	m.Prepare(context.Background(), nil)
	if client.calls != 0 {
		t.Errorf("below the auto line nothing may summarize: calls = %d", client.calls)
	}
}

func TestComputeKeepBoundary(t *testing.T) {
	est := NewEstimator("", stubSchemas(nil))
	msg := func(chars int) conversation.Message {
		return conversation.Message{Role: conversation.RoleUser, Content: strings.Repeat("a", chars)}
	}
	toolResult := func(id string) conversation.Message {
		return conversation.Message{ToolResults: []conversation.ToolResultBlock{{ToolUseID: id, Content: "r"}}}
	}
	toolUse := func(id string) conversation.Message {
		return conversation.Message{ToolUses: []conversation.ToolUseBlock{{ID: id, Name: "Read"}}}
	}

	tests := []struct {
		name string
		msgs []conversation.Message
		want int
	}{
		{
			// Six ~3k-token messages: the walk-back stops once both floors
			// hold (≥10k tokens and ≥5 messages).
			name: "floors",
			msgs: []conversation.Message{msg(12000), msg(12000), msg(12000), msg(12000), msg(12000), msg(12000)},
			want: 1,
		},
		{
			// ~15k-token messages: the 40k cap breaks the walk after two.
			name: "cap after first message",
			msgs: []conversation.Message{msg(60000), msg(60000), msg(60000), msg(60000), msg(60000), msg(60000)},
			want: 4,
		},
		{
			// The boundary lands on a tool_result whose call sits outside; the
			// walk snaps back one message to include the tool_use.
			name: "orphan tool_result snaps back",
			msgs: []conversation.Message{
				msg(60000), toolUse("t1"), toolResult("t1"),
				msg(12000), msg(12000), msg(12000), msg(12000),
			},
			want: 1,
		},
		{
			// Snapping back would reach index 0 with nothing kept: abort.
			name: "orphan reaching the head aborts",
			msgs: []conversation.Message{
				toolUse("t1"), toolResult("t1"),
				msg(12000), msg(12000), msg(12000), msg(12000),
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeKeepBoundary(est, tt.msgs); got != tt.want {
				t.Errorf("computeKeepBoundary() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRecovery(t *testing.T) {
	tests := []struct {
		name     string
		recovery func() string
		want     string
	}{
		{name: "no provider", recovery: nil},
		{name: "an empty answer changes nothing", recovery: func() string { return "   \n" }},
		{name: "the provider's text rides the boundary", recovery: func() string { return "[aether:skill commit] 按它执行" },
			want: "[aether:skill commit] 按它执行"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := breakerManager(t, 6, 8000)
			client, ok := m.client.(*stubClient)
			if !ok {
				t.Fatal("the breaker fixture changed shape")
			}
			client.fail = false
			m.recovery = tt.recovery

			before := m.conv.Messages()[0].Content
			result, err := m.ForceCompact(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Compacted {
				t.Fatal("the fixture did not compact")
			}
			msgs := m.conv.Messages()
			if msgs[0].Content == before {
				t.Fatal("the head of the history survived the rewrite")
			}
			boundary := msgs[0]
			if boundary.Role != conversation.RoleUser {
				t.Errorf("the boundary is a %s message", boundary.Role)
			}
			if !strings.Contains(boundary.Content, "</summary>") {
				t.Fatalf("the boundary lost its summary:\n%s", boundary.Content)
			}
			if tt.want == "" {
				if strings.Contains(boundary.Content, "[aether:skill") {
					t.Errorf("a provider with nothing to say must leave the boundary alone:\n%s", boundary.Content)
				}
				return
			}
			// It lands after the summary it belongs beside, and before the tail
			// that was kept verbatim.
			if at, summary := strings.Index(boundary.Content, tt.want), strings.Index(boundary.Content, "</summary>"); at < summary {
				t.Errorf("the recovered text is at %d, before the summary ends at %d", at, summary)
			}
			if !strings.Contains(boundary.Content, tt.want) {
				t.Errorf("the boundary misses the recovered text:\n%s", boundary.Content)
			}
		})
	}
}

func TestExtractSummary(t *testing.T) {
	tests := []struct {
		name  string
		reply string
		want  string
	}{
		{"both tags", "<analysis>draft</analysis>\n<summary>body</summary>", "body"},
		{"no tags keeps raw", "plain answer", "plain answer"},
		{"missing close keeps raw", "<summary>body", "<summary>body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractSummary(tt.reply); got != tt.want {
				t.Errorf("extractSummary(%q) = %q, want %q", tt.reply, got, tt.want)
			}
		})
	}
}
