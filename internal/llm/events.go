package llm

type StreamEvent interface{ streamEvent() }

type TextDelta struct{ Text string }
type TextComplete struct{}

type ThinkingDelta struct{ Text string }
type ThinkingComplete struct {
	Thinking  string
	Signature string
}

type ToolCallComplete struct {
	ToolID    string
	ToolName  string
	Arguments map[string]any
}

type Usage struct {
	InputTokens  int
	OutputTokens int
	// The two cache fields are NOT counted in InputTokens by Anthropic, so the
	// true prompt size is InputTokens + CacheReadTokens + CacheCreationTokens.
	CacheReadTokens     int
	CacheCreationTokens int
}

type StreamEnd struct {
	Usage Usage
}

type StreamError struct{ Err error }

func (TextDelta) streamEvent()        {}
func (TextComplete) streamEvent()     {}
func (ThinkingDelta) streamEvent()    {}
func (ThinkingComplete) streamEvent() {}
func (ToolCallComplete) streamEvent() {}
func (StreamEnd) streamEvent()        {}
func (StreamError) streamEvent()      {}
