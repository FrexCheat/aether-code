// Package llm — events: provider-agnostic streaming event type.
//
// Providers translate their wire events into this shape. Callers observe a
// single in-order stream that ends with exactly one StreamEnd or StreamError,
// after which the channel is closed.
package llm

// StreamEvent is the sealed sum of all events emitted by Client.Stream.
//
// Implementations include TextDelta, ThinkingDelta, ThinkingComplete,
// ToolCallStart, ToolCallDelta, ToolCallComplete, StreamEnd and StreamError.
// Consumers should use a type switch.
type StreamEvent interface{ streamEvent() }

type TextDelta struct{ Text string }
type ThinkingDelta struct{ Text string }
type ThinkingComplete struct {
	Thinking  string
	Signature string
}
type ToolCallStart struct{ ToolName, ToolID string }
type ToolCallDelta struct{ Text string }
type ToolCallComplete struct {
	ToolID    string
	ToolName  string
	Arguments map[string]any
}
type UsageInfo struct {
	InputTokens  int
	OutputTokens int
	// CacheReadTokens is the number of input tokens served from the prompt
	// cache (Anthropic cache_read_input_tokens; OpenAI
	// prompt_tokens_details.cached_tokens). These are NOT counted in
	// InputTokens by Anthropic, so the true prompt size is
	// InputTokens + CacheReadTokens + CacheCreationTokens.
	CacheReadTokens int
	// CacheCreationTokens is the number of input tokens written into the
	// prompt cache this turn (Anthropic cache_creation_input_tokens). Zero
	// for providers that don't report it.
	CacheCreationTokens int
}

type StreamEnd struct {
	StopReason string
	Usage      UsageInfo
}

// StreamError is the terminal error event. It is mutually exclusive with
// StreamEnd: exactly one of them is the last event before the channel closes.
type StreamError struct{ Err error }

func (TextDelta) streamEvent()        {}
func (ThinkingDelta) streamEvent()    {}
func (ThinkingComplete) streamEvent() {}
func (ToolCallStart) streamEvent()    {}
func (ToolCallDelta) streamEvent()    {}
func (ToolCallComplete) streamEvent() {}
func (StreamEnd) streamEvent()        {}
func (StreamError) streamEvent()      {}
