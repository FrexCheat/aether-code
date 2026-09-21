package compaction

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
)

const (
	outputReserveTokens = 20000
	autoCompactMargin   = 13000
	forceMargin         = 3000

	keepRecentTokens = 10000
	keepMinMessages  = 5
	keepMaxTokens    = 40000
)

// compact summarizes history[:keepStart] into one boundary user message and
// keeps the tail verbatim. The caller owns locking; auto toggles whether a
// failure feeds the circuit breaker.
func (m *Manager) compact(ctx context.Context, notify func(Event), auto bool) (Result, error) {
	msgs := m.conv.Messages()
	keepStart := computeKeepBoundary(msgs)
	from := m.usedTokens()
	if keepStart <= 0 {
		return Result{FromTokens: from, ToTokens: from}, nil
	}
	if notify != nil {
		notify(StartEvent{Auto: auto})
	}
	reply, err := m.summarize(ctx, msgs[:keepStart])
	if err != nil {
		if auto {
			m.failures++
		}
		if notify != nil {
			notify(FailEvent{Auto: auto, Err: err})
		}
		return Result{FromTokens: from}, fmt.Errorf("compaction: summary failed: %w", err)
	}
	m.conv.ReplacePrefix(conversation.Message{
		Role:    conversation.RoleUser,
		Content: boundaryMessage(extractSummary(reply), keepStart < len(msgs)),
	}, keepStart)
	// The old anchor counts a history that no longer exists.
	m.anchor = UsageAnchor{}
	m.failures = 0
	res := Result{Compacted: true, FromTokens: from, ToTokens: m.usedTokens()}
	if notify != nil {
		notify(DoneEvent{Auto: auto, FromTokens: res.FromTokens, ToTokens: res.ToTokens})
	}
	return res, nil
}

// computeKeepBoundary walks back from the tail until both floors hold
// (keepRecentTokens AND keepMinMessages); the cap only applies once the
// message floor is met, and the newest message is kept unconditionally. The
// boundary then snaps back so the tail never opens with a tool_result whose
// call lives in the summary region — EnsureToolPairing would drop it.
func computeKeepBoundary(msgs []conversation.Message) int {
	acc, count, keepStart := 0, 0, 0
	for i, msg := range slices.Backward(msgs) {
		size := EstimateTokens(msg)
		if count > 0 && acc+size > keepMaxTokens {
			break
		}
		acc += size
		count++
		keepStart = i
		if acc >= keepRecentTokens && count >= keepMinMessages {
			break
		}
	}
	if keepStart == 0 {
		return 0
	}

	uses := make(map[string]struct{})
	for _, msg := range msgs[keepStart:] {
		for _, tu := range msg.ToolUses {
			uses[tu.ID] = struct{}{}
		}
	}
	for keepStart > 0 {
		results := msgs[keepStart].ToolResults
		if len(results) == 0 {
			break
		}
		orphan := false
		for _, tr := range results {
			if _, ok := uses[tr.ToolUseID]; !ok {
				orphan = true
				break
			}
		}
		if !orphan {
			break
		}
		keepStart--
		for _, tu := range msgs[keepStart].ToolUses {
			uses[tu.ID] = struct{}{}
		}
	}
	return keepStart
}

// summarize sends the doomed region plus the summary prompt through the same
// client and tools array as the main loop, so the request prefix — tools and
// system prompt — stays byte-identical and hits the prompt cache. Only text
// events are consumed; a tool call on the wire is ignored, the prompt already
// forbids them.
func (m *Manager) summarize(ctx context.Context, region []conversation.Message) (string, error) {
	msgs := append(slices.Clone(region), conversation.Message{
		Role:    conversation.RoleUser,
		Content: summaryPrompt,
	})
	var text strings.Builder
	for event := range m.client.Stream(ctx, llm.Request{
		Messages: conversation.EnsureToolPairing(msgs),
		Tools:    m.schemas(),
	}) {
		switch e := event.(type) {
		case llm.TextDelta:
			text.WriteString(e.Text)
		case llm.StreamEnd:
			return text.String(), nil
		case llm.StreamError:
			return "", e.Err
		}
	}
	return "", errors.New("compaction: summary stream ended unexpectedly")
}

// extractSummary drops the analysis draft and takes the summary body; when
// the tags are missing the raw reply wins — a format slip must never discard
// a summary the model already paid for.
func extractSummary(reply string) string {
	if before, rest, ok := strings.Cut(reply, "<analysis>"); ok {
		if _, after, closed := strings.Cut(rest, "</analysis>"); closed {
			reply = before + after
		}
	}
	if _, rest, ok := strings.Cut(reply, "<summary>"); ok {
		if body, _, closed := strings.Cut(rest, "</summary>"); closed {
			return strings.TrimSpace(body)
		}
	}
	return strings.TrimSpace(reply)
}

func boundaryMessage(summary string, tail bool) string {
	var b strings.Builder
	b.WriteString("本次会话延续自之前的对话：上下文空间不足，早期对话已被压缩为下面的摘要。")
	if tail {
		b.WriteString("之后的近期消息已原样保留。")
	}
	b.WriteString("\n\n<summary>\n")
	b.WriteString(summary)
	b.WriteString("\n</summary>\n\n")
	b.WriteString("摘要只是压缩描述，细节可能失真。当需要确切信息时（文件的当前内容、命令的原始输出、代码原文），用工具重新读取文件或重新执行操作，不要依据摘要重构。")
	return b.String()
}
