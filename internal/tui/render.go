package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/FrexCheat/aether-code/internal/agent"
)

const (
	statusBarHeight = 1
	toolIndent      = "  "
)

func renderTop(m *Model) string {
	var b strings.Builder
	b.WriteString(renderBanner(m.opts.Version, m.opts.WorkDir, m.width))
	b.WriteString("\n\n")
	b.WriteString(renderReady())
	b.WriteString("\n\n")
	for i := range m.entries {
		renderEntry(&b, m, &m.entries[i])
		b.WriteString("\n")
	}
	if m.live != nil {
		renderLive(&b, m)
	}
	return b.String()
}

func renderEntry(b *strings.Builder, m *Model, e *entry) {
	switch e.kind {
	case entryUser:
		b.WriteString(styleUserBubble.Width(m.width).Render(e.text))
		b.WriteString("\n")
	case entryAssistant:
		out := e.cachedRender(m.width, func(md string) string {
			if m.renderer == nil {
				return md
			}
			r, err := m.renderer.Render(md)
			if err != nil {
				return md
			}
			return strings.Trim(r, "\n")
		})
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, styleAssistantDot.Render("●")+" ", out))
		b.WriteString("\n")
	case entryNotice:
		b.WriteString(styleNoticePrefix.Render("! AetherCode"))
		b.WriteString("\n")
		switch e.severity {
		case noticeError:
			b.WriteString(styleError.Render(e.text))
		case noticeWarn:
			b.WriteString(styleWarn.Render(e.text))
		default:
			b.WriteString(e.text)
		}
		b.WriteString("\n")
	case entryTool:
		renderToolEntry(b, m, e)
	case entryTurn:
		b.WriteString(styleDim.Render(fmt.Sprintf("%ds · done %s", int(e.elapsed.Seconds()), e.doneAt.Format("3:04 PM"))))
		b.WriteString("\n")
	}
}

func renderToolEntry(b *strings.Builder, m *Model, e *entry) {
	switch e.state {
	case toolRunning:
		b.WriteString(toolIndent)
		b.WriteString(styleSpinner.Render(m.spinner.View()))
		b.WriteString(" ")
		b.WriteString(e.text)
		b.WriteString("\n")
	case toolDone, toolFailed:
		dot := styleToolDone.Render("●")
		if e.state == toolFailed {
			dot = styleToolFailed.Render("●")
		}
		var body strings.Builder
		body.WriteString(e.text)
		body.WriteString(styleDim.Render(fmt.Sprintf(" (%.1fs)", e.elapsed.Seconds())))
		if e.diff != "" {
			body.WriteString("\n")
			body.WriteString(renderDiffLines(e.diff))
		}
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, toolIndent+dot+" ", body.String()))
		b.WriteString("\n")
	}
}

// renderDiffLines colors BuildDiff output line by line: "- " removals red,
// "+ " additions green, everything else (context / header lines) dim.
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

func renderLive(b *strings.Builder, m *Model) {
	secs := int(timeSince(m.live.start).Seconds())
	progress := fmt.Sprintf("Imagining… (%ds)", secs)
	if m.live.turn > 0 {
		progress = fmt.Sprintf("Imagining… (Turn %d · %ds)", m.live.turn, secs)
	}
	b.WriteString(styleSpinner.Render(m.spinner.View() + " " + progress))
	b.WriteString("\n")
}

func (m Model) renderInput() string {
	return styleInputBox.Width(max(m.width-2, 10)).Render(m.input.View())
}

func (m Model) renderStatus() string {
	left := " " + m.opts.ProviderName
	if m.opts.Agent.Mode() == agent.ModePlan {
		left += " [plan]"
	}
	right := m.opts.ModelName + " "
	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return styleStatusBar.Width(m.width).Render(left + strings.Repeat(" ", pad) + right)
}

func (m Model) View() tea.View {
	content := lipgloss.JoinVertical(lipgloss.Left,
		m.viewport.View(),
		m.renderInput(),
		m.renderStatus(),
	)
	v := tea.NewView(content)
	v.AltScreen = false
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
