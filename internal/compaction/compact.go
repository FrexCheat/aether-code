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
	keepRecentTokens = 10000
	keepMinMessages  = 5
	keepMaxTokens    = 40000
)

const summaryPrompt = `严格禁止调用任何工具：不要调用任何工具，工具调用会被直接拒绝，并浪费你唯一的一次回复机会。你只能输出文本。

下面是一次编码会话的对话历史。由于即将超出上下文窗口，其中较早的消息将被丢弃，你的任务是输出一份结构化摘要，让后续对话能够无缝继续。逐条完成以下小节，全部使用中文（代码、命令、路径、标识符保持原样）：

1. 主要请求与意图：用户想让助手做什么？逐条列出意图与目标，包括后续的修正与补充。
2. 关键技术概念：涉及的技术、库、框架、协议与约定，各附一句说明。
3. 文件与代码段：列出创建或修改过的每个文件及其路径、改动要点；关键代码用代码块原样保留（含 import、函数签名与关键行），不得凭记忆改写。
4. 错误与修复：列出遇到过的错误、报错要点、根因与最终修法；注明尚未解决的错误。
5. 问题求解：为解决开放问题尝试过什么、哪些路线可行、哪些已被排除。
6. 所有用户消息：按顺序完整逐字引用每一条用户消息，不得改写、不得概括、不得省略任何一条；<system-reminder> 包裹的系统信息不算用户消息。
7. 待办事项：用户已提出但尚未完成的事项，注明哪些已完成。
8. 当前工作：截断时刻正在做什么，精确到最后一步操作与相关文件的当前状态。
9. 下一步（可选）：仅当对话中已有明确的下一步时，直接引用用户或助手的原话说明；否则省略本节。

输出格式（严格遵守）：先输出 <analysis> 标签，在其中起草上述各节并自查完整性（该草稿随后会被丢弃）；确认无误后输出 <summary> 标签，给出最终摘要正文。<summary> 结束后不得再有任何文字。再次强调：全程禁止任何工具调用。`

// compact summarizes history[:keepStart] into one boundary user message and
// keeps the tail verbatim. The caller owns locking; auto marks the
// pre-request path in the progress events, while band decides which
// circuit-breaker counter a failure feeds.
func (m *Manager) compact(ctx context.Context, notify func(Progress), auto bool, band failBand) (Result, error) {
	msgs := m.conv.Messages()
	keepStart := computeKeepBoundary(m.estimator, msgs)
	from := m.usedTokens()
	if keepStart <= 0 {
		return Result{FromTokens: from, ToTokens: from}, nil
	}
	if notify != nil {
		notify(Progress{Phase: PhaseStart, Auto: auto})
	}
	reply, err := m.summarize(ctx, msgs[:keepStart])
	if err != nil {
		m.countFailure(band)
		if notify != nil {
			notify(Progress{Phase: PhaseFailed, Auto: auto, Err: err})
		}
		return Result{FromTokens: from}, fmt.Errorf("compaction: summary failed: %w", err)
	}
	// What the summary replaced but did not restate goes back in the same
	// message: the boundary is where the model reads that its context was
	// rewritten, so it is where the ledger of what it was told has to be
	// restored. It rides the boundary record, which is why a resume replays it
	// without anything extra.
	extra := ""
	if m.recovery != nil {
		extra = strings.TrimSpace(m.recovery())
	}
	if err := m.conv.ReplacePrefix(conversation.Message{
		Role:    conversation.RoleUser,
		Content: boundaryMessage(extractSummary(reply), extra, keepStart < len(msgs)),
	}, keepStart); err != nil {
		// A refused rewrite is a summary failure for the breaker's purposes:
		// without this every round would re-run a full summarize call while
		// the archive is broken.
		m.countFailure(band)
		if notify != nil {
			notify(Progress{Phase: PhaseFailed, Auto: auto, Err: err})
		}
		return Result{FromTokens: from}, fmt.Errorf("compaction: cannot rewrite the prefix: %w", err)
	}
	// The old anchor counts a history that no longer exists.
	m.anchor = UsageAnchor{}
	m.failures = 0
	m.forceFailures = 0
	m.publish()
	res := Result{Compacted: true, FromTokens: from, ToTokens: m.usedTokens()}
	if notify != nil {
		notify(Progress{Phase: PhaseDone, Auto: auto, FromTokens: res.FromTokens, ToTokens: res.ToTokens})
	}
	return res, nil
}

// computeKeepBoundary walks back from the tail until both floors hold
// (keepRecentTokens AND keepMinMessages); the cap only applies once the
// message floor is met, and the newest message is kept unconditionally. The
// boundary then snaps back so the tail never opens with a tool_result whose
// call lives in the summary region — EnsureToolPairing would drop it.
func computeKeepBoundary(est *Estimator, msgs []conversation.Message) int {
	acc, count, keepStart := 0, 0, 0
	for i, msg := range slices.Backward(msgs) {
		size := est.Message(msg)
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

func boundaryMessage(summary, extra string, tail bool) string {
	var b strings.Builder
	b.WriteString("本次会话延续自之前的对话：上下文空间不足，早期对话已被压缩为下面的摘要。")
	if tail {
		b.WriteString("之后的近期消息已原样保留。")
	}
	b.WriteString("\n\n<summary>\n")
	b.WriteString(summary)
	b.WriteString("\n</summary>\n\n")
	b.WriteString("摘要只是压缩描述，细节可能失真。当需要确切信息时（文件的当前内容、命令的原始输出、代码原文），用工具重新读取文件或重新执行操作，不要依据摘要重构。")
	if extra != "" {
		b.WriteString("\n\n")
		b.WriteString(extra)
	}
	return b.String()
}
