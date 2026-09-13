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
)

// renderTop derives the viewport content for the whole upper region: banner,
// ready hint, every committed entry, and the live spinner line while a turn is
// in flight. It is a pure function: it reads entries and writes nothing.
func renderTop(m *Model) string {
	var b strings.Builder
	b.WriteString(renderBanner(m.opts.Version, m.opts.WorkDir, m.width))
	b.WriteString("\n\n")
	b.WriteString(renderReady())
	b.WriteString("\n\n")
	for i := range m.entries {
		renderEntry(&b, m, &m.entries[i])
	}
	if m.live != nil {
		renderLive(&b, m)
	}
	return b.String()
}

func renderEntry(b *strings.Builder, m *Model, e *entry) {
	switch e.kind {
	case entryUser:
		b.WriteString(styleUserPrefix.Render("❯ You"))
		b.WriteString("\n")
		b.WriteString(e.text)
		b.WriteString("\n\n")
	case entryAssistant:
		b.WriteString(styleAssistantPrefix.Render("● AetherCode"))
		b.WriteString("\n")
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
		b.WriteString(out)
		b.WriteString("\n\n")
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
		b.WriteString("\n\n")
	case entryTool:
		renderToolEntry(b, m, e)
	case entryTurn:
		// The completion form of the live spinner timer: one footer per agent
		// loop, not per message.
		b.WriteString(styleDim.Render(fmt.Sprintf("%ds · done %s",
			int(e.elapsed.Seconds()), e.doneAt.Format("3:04 PM"))))
		b.WriteString("\n\n")
	}
}

// renderToolEntry draws one tool call: the "● title" header with an inline
// spinner while running, then "⎿ ✓/✗ title (elapsed)" with the edit diff
// colored line by line once settled — mewcode's presentation.
func renderToolEntry(b *strings.Builder, m *Model, e *entry) {
	switch e.state {
	case toolRunning:
		b.WriteString(styleAssistantPrefix.Render("● "))
		b.WriteString(e.text)
		b.WriteString(" " + m.spinner.View())
	case toolDone, toolFailed:
		b.WriteString(styleDim.Render("  ⎿ "))
		if e.state == toolDone {
			b.WriteString(styleToolDone.Render("✓ " + e.text))
		} else {
			b.WriteString(styleToolFailed.Render("✗ " + e.text))
		}
		b.WriteString(styleDim.Render(fmt.Sprintf(" (%.1fs)", e.elapsed.Seconds())))
		b.WriteString("\n")
		if e.diff != "" {
			b.WriteString(renderDiffLines(e.diff))
			b.WriteString("\n")
		}
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

// renderLive shows the assistant marker plus the bubbles spinner, the current
// turn number once the loop has executed tool rounds, and the whole-loop
// elapsed seconds at the bottom of the in-flight turn — the loop is the only
// unit the timer measures (tool entries carry their own).
func renderLive(b *strings.Builder, m *Model) {
	b.WriteString(styleAssistantPrefix.Render("● AetherCode"))
	b.WriteString("\n")
	secs := int(timeSince(m.live.start).Seconds())
	progress := fmt.Sprintf("Imagining… (%ds)", secs)
	if m.live.turn > 0 {
		progress = fmt.Sprintf("Imagining… (Turn %d · %ds)", m.live.turn, secs)
	}
	b.WriteString(styleSpinner.Render(m.spinner.View() + " " + progress))
	b.WriteString("\n\n")
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

// View implements tea.Model. It is a pure read of the current Model: all
// content was pushed to the viewport by refresh during Update. The screen runs
// on the main buffer (no alt-screen) so Shift-drag selects/copies terminal
// text natively; mouse events are captured (CellMotion) for wheel scrolling.
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
