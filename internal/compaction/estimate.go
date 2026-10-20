package compaction

import (
	"encoding/json"
	"sync"
	"unicode/utf8"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/tools"
)

// UsageAnchor pins the last real API accounting: BaselineTokens is that
// request's full prompt plus output, AnchorCount the history length it
// covered. Everything appended after the anchor is only estimated.
type UsageAnchor struct {
	BaselineTokens int
	AnchorCount    int
}

// unknownToolPrice prices a tool_reference the registry does not know (a
// replayed result naming a tool no longer on the wire); a definition that
// cannot be seen is overestimated, the safe direction.
const unknownToolPrice = 200

// Estimator approximates the token counts no API usage covers. ASCII text
// runs about four characters per token; every other rune counts as one
// token — CJK measures roughly one token per character, and the bytes/3.5
// rule this replaces undercounted it by half, which the model-facing text
// of this program is full of.
type Estimator struct {
	systemPrompt string
	schemas      func() []tools.ToolSchema

	mu         sync.Mutex
	toolTokens map[string]int
}

func NewEstimator(systemPrompt string, schemas func() []tools.ToolSchema) *Estimator {
	return &Estimator{systemPrompt: systemPrompt, schemas: schemas}
}

// FixedPrefix is only used without an anchor; MCP can change the tool set later.
func (e *Estimator) FixedPrefix() int {
	total := estimateTokens(e.systemPrompt)
	for _, s := range e.schemas() {
		total += e.toolPrice(s.Name)
	}
	return total
}

func (e *Estimator) Messages(msgs []conversation.Message) int {
	total := 0
	for _, msg := range msgs {
		total += e.Message(msg)
	}
	return total
}

func (e *Estimator) Message(msg conversation.Message) int {
	total := estimateTokens(msg.Content) + 4
	for _, tr := range msg.ToolResults {
		total += estimateTokens(tr.Content) + 10
		// The Anthropic API expands every tool_reference into the referenced
		// tool's full definition, which the result's own text does not carry.
		for _, name := range tr.ToolRefs {
			total += e.toolPrice(name)
		}
	}
	for _, tu := range msg.ToolUses {
		args, _ := json.Marshal(tu.Args)
		total += 50 + estimateTokens(string(args))
	}
	for _, tb := range msg.ThinkingBlocks {
		// Every part of a reasoning block rides the request: the two text
		// channels plus the opaque payload.
		total += estimateTokens(tb.Text) + estimateTokens(tb.Content) + estimateTokens(tb.Encrypted)
	}
	return total
}

// toolPrice prices one tool's full wire definition. The lookup table is
// built lazily and rebuilt on a miss, so tools registered after the session
// starts — the MCP settle — heal themselves.
func (e *Estimator) toolPrice(name string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if price, ok := e.toolTokens[name]; ok {
		return price
	}
	e.toolTokens = make(map[string]int)
	for _, s := range e.schemas() {
		schema, _ := json.Marshal(s.InputSchema)
		e.toolTokens[s.Name] = 50 + estimateTokens(s.Name) +
			estimateTokens(s.Description) + estimateTokens(string(schema))
	}
	if price, ok := e.toolTokens[name]; ok {
		return price
	}
	return unknownToolPrice
}

func estimateTokens(text string) int {
	ascii, other := 0, 0
	for _, r := range text {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			other++
		}
	}
	return ascii/4 + other
}
