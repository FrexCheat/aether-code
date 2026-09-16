package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/FrexCheat/aether-code/internal/llm"
)

const toolIndent = "  "

type noticeSeverity int

const (
	noticeInfo noticeSeverity = iota
	noticeWarn
	noticeError
)

func totalTokens(u llm.UsageInfo) int {
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheCreationTokens
}

func formatTokenCount(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	k := strconv.FormatFloat(float64(n)/1000, 'f', 1, 64)
	return strings.TrimSuffix(k, ".0") + "k"
}

func renderNoticeBlock(text string, severity noticeSeverity) string {
	var b strings.Builder
	b.WriteString(styleNoticePrefix.Render("! AetherCode"))
	b.WriteString("\n")
	switch severity {
	case noticeError:
		b.WriteString(styleError.Render(text))
	case noticeWarn:
		b.WriteString(styleWarn.Render(text))
	default:
		b.WriteString(text)
	}
	return b.String()
}

func renderUserBlock(m *Model, text string) string {
	return styleUserBubble.Width(m.width).Render(text)
}

func renderAssistantBlock(m *Model, text string) string {
	out := text
	if m.renderer != nil {
		if r, err := m.renderer.Render(text); err == nil {
			out = strings.Trim(r, "\n")
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, styleAssistantDot.Render("●")+" ", out)
}

func renderToolRunningBlock(m *Model, tc toolCall) string {
	return toolIndent + styleSpinner.Render(m.spinner.View()) + " " + tc.title
}

func renderToolDoneBlock(title string, failed bool, elapsed time.Duration, diff string) string {
	dot := styleToolDone.Render("●")
	if failed {
		dot = styleToolFailed.Render("●")
	}
	var body strings.Builder
	body.WriteString(title)
	body.WriteString(styleDim.Render(fmt.Sprintf(" (%.1fs)", elapsed.Seconds())))
	if diff != "" {
		body.WriteString("\n")
		body.WriteString(renderDiffLines(diff))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, toolIndent+dot+" ", body.String())
}

func renderTurnBlock(elapsed time.Duration, doneAt time.Time, usage llm.UsageInfo) string {
	line := fmt.Sprintf("%ds · done %s · ↓ %s tokens",
		int(elapsed.Seconds()), doneAt.Format("3:04 PM"), formatTokenCount(totalTokens(usage)))
	if usage.CacheReadTokens > 0 {
		line += " · cache " + formatTokenCount(usage.CacheReadTokens)
	}
	return styleDim.Render(line)
}

func renderClearedBlock() string {
	return styleDim.Render("— context cleared —")
}

func renderDiffLines(diff string) string {
	lines := strings.Split(diff, "\n")
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "+"):
			lines[i] = styleDiffAdd.Render(line)
		case strings.HasPrefix(line, "-"):
			lines[i] = styleDiffRemove.Render(line)
		default:
			lines[i] = styleDim.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}

func renderLive(m *Model) string {
	secs := int(timeSince(m.live.start).Seconds())
	return styleSpinner.Render(m.spinner.View() + fmt.Sprintf(" Imagining… (%ds)", secs))
}

func (m Model) renderInput() string {
	return styleInputBox.Width(max(m.width-2, 10)).Render(m.input.View())
}

func (m Model) renderStatus() string {
	left := " " + m.opts.ProviderName
	right := m.opts.ModelName + " "
	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return styleStatusBar.Width(m.width).Render(left + strings.Repeat(" ", pad) + right)
}

func (m Model) View() tea.View {
	var parts []string
	for _, tc := range m.running {
		parts = append(parts, renderToolRunningBlock(&m, tc))
	}
	if m.live != nil {
		parts = append(parts, renderLive(&m))
	}
	parts = append(parts, m.renderInput(), m.renderStatus())
	v := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, parts...))
	v.AltScreen = false
	v.MouseMode = tea.MouseModeNone
	return v
}
