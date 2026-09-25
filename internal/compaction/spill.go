package compaction

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/FrexCheat/aether-code/internal/conversation"
)

const (
	spillThresholdChars = 50000
	spillAggregateChars = 200000
	spillPreviewChars   = 2000
)

// spillMarker opens every spill file; its presence anywhere in a tool result
// means the text was read back from a spill file (Read line numbers, cat,
// grep hits all carry it mid-content), so matching must use Contains.
const spillMarker = "aethercode spilled tool result"

const persistedOpenTag = "<persisted-output>"

func spillFileHeader(toolUseID string, chars int) string {
	return fmt.Sprintf(
		"%s：一次工具调用的完整输出，因超出大小阈值未保留在会话上下文中。\n工具调用 ID: %s\n字符数: %d\n\n",
		spillMarker, toolUseID, chars)
}

func persistedReplacement(chars int, path, preview string) string {
	return fmt.Sprintf(`%s
该工具输出过大（%d 字符），未保留在上下文中，已完整写入文件：
%s

前 %d 字符预览：
%s
需要完整内容时，用 Read 工具读取上述文件（可配合 offset/limit 分段读取）。
</persisted-output>`, persistedOpenTag, chars, path, len([]rune(preview)), preview)
}

// spillOversized rewrites oversized tool results in the stored history to
// preview-plus-path. Replacements are byte-stable, so repeated scans are
// no-ops.
func (m *Manager) spillOversized() {
	for _, msg := range m.conv.Messages() {
		if len(msg.ToolResults) == 0 {
			continue
		}
		m.spillMessage(msg.ToolResults)
	}
}

// spillMessage serves both thresholds from one loop: while any single
// non-exempt result exceeds spillThresholdChars, or the combined size
// exceeds spillAggregateChars, the largest result is spilled. Spilling the
// largest drives both metrics down, which is exactly the shape of a parallel
// read-only batch (many medium results committed as one message).
func (m *Manager) spillMessage(results []conversation.ToolResultBlock) {
	candidates := make([]int, 0, len(results))
	for i, tr := range results {
		if !spillExempt(tr) {
			candidates = append(candidates, i)
		}
	}
	for len(candidates) > 0 {
		total, maxLen, maxIdx := 0, 0, -1
		for _, i := range candidates {
			if n := len(results[i].Content); n > maxLen {
				maxLen, maxIdx = n, i
			}
			total += len(results[i].Content)
		}
		if maxLen <= spillThresholdChars && total <= spillAggregateChars {
			return
		}
		m.spill(results[maxIdx])
		candidates = slices.DeleteFunc(candidates, func(i int) bool { return i == maxIdx })
	}
}

// spillExempt marks results that must never be spilled: ToolSearch results
// carry the tool_reference expansion on the wire, and text holding the spill
// marker or the replacement tag has already been through a spill round.
func spillExempt(tr conversation.ToolResultBlock) bool {
	if len(tr.ToolRefs) > 0 {
		return true
	}
	return strings.Contains(tr.Content, spillMarker) || strings.Contains(tr.Content, persistedOpenTag)
}

// spill writes the full original to disk and rewrites the history entry. Any
// failure keeps the original in the history — losing the model's tool result
// is worse than losing the tokens, so a refused journal write is fail-open
// here the same way.
func (m *Manager) spill(tr conversation.ToolResultBlock) string {
	path, err := m.writeSpillFile(tr.ToolUseID, tr.Content)
	if err != nil {
		return ""
	}
	preview := truncatePreview(tr.Content, spillPreviewChars)
	if _, err := m.conv.ReplaceToolResult(tr.ToolUseID, persistedReplacement(len(tr.Content), path, preview)); err != nil {
		return ""
	}
	return path
}

// writeSpillFile persists the full result under the session's spill
// directory. The content for one tool-use ID is deterministic, so an existing
// file is either the same spill or a truncated leftover from a crash: it is
// rewritten in place instead of versioned, which also keeps a resume
// re-spill byte-identical.
func (m *Manager) writeSpillFile(toolUseID, content string) (string, error) {
	if err := os.MkdirAll(m.spillDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(m.spillDir, toolUseID+".txt")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(spillFileHeader(toolUseID, len(content)) + content); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, nil
}

func truncatePreview(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max])
}
