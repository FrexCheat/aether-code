package compaction

import (
	"encoding/json"

	"github.com/FrexCheat/aether-code/internal/conversation"
)

// UsageAnchor pins the last real API accounting: BaselineTokens is that
// request's full prompt plus output, AnchorCount the history length it
// covered. Everything appended after the anchor is only estimated.
type UsageAnchor struct {
	BaselineTokens int
	AnchorCount    int
}

// tokensForChars divides by 3.5 (x2/7 in integers). Code-heavy ASCII output
// measures ~3.8-4.2 chars/token for real tokenizers, so the ratio runs
// deliberately low: overestimating is safe, underestimating is not.
func tokensForChars(chars int) int {
	return chars * 2 / 7
}

// EstimateTokens approximates one message's wire cost. The per-entry fudges
// are structural overheads measured in mewcode, kept without independent
// tuning.
func EstimateTokens(msg conversation.Message) int {
	total := tokensForChars(len(msg.Content)) + 4
	for _, tr := range msg.ToolResults {
		total += tokensForChars(len(tr.Content)) + 10
	}
	for _, tu := range msg.ToolUses {
		args, _ := json.Marshal(tu.Args)
		total += 50 + tokensForChars(len(args))
	}
	for _, tb := range msg.ThinkingBlocks {
		total += tokensForChars(len(tb.Text))
	}
	return total
}

func EstimateMessages(msgs []conversation.Message) int {
	total := 0
	for _, msg := range msgs {
		total += EstimateTokens(msg)
	}
	return total
}
