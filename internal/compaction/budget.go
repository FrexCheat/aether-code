package compaction

import (
	"errors"
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
// is worse than losing the tokens.
func (m *Manager) spill(tr conversation.ToolResultBlock) string {
	path, err := m.writeSpillFile(tr.ToolUseID, tr.Content)
	if err != nil {
		return ""
	}
	preview := truncatePreview(tr.Content, spillPreviewChars)
	m.conv.ReplaceToolResult(tr.ToolUseID, persistedReplacement(len(tr.Content), path, preview))
	return path
}

func (m *Manager) writeSpillFile(toolUseID, content string) (string, error) {
	if err := os.MkdirAll(m.spillDir, 0o755); err != nil {
		return "", err
	}
	// toolUseID is unique per session only; the numbered suffixes absorb the
	// rare collision after a pid reuse.
	for _, suffix := range []string{"", "-2", "-3", "-4", "-5"} {
		path := filepath.Join(m.spillDir, toolUseID+suffix+".txt")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
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
	return "", errors.New("compaction: spill file name exhausted")
}

func truncatePreview(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max])
}
