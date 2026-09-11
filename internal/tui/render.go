package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	inputBoxHeight  = 3 // border top + content + border bottom
	statusBarHeight = 1
)

// renderTranscript derives the whole chat area from the transcript. It is a
// pure function: it reads items and writes nothing.
func renderTranscript(m *Model) string {
	var b strings.Builder
	for _, it := range m.transcript.Items() {
		switch it := it.(type) {
		case *Turn:
			renderTurn(&b, m, it)
		case *Notice:
			b.WriteString(styleNoticePrefix.Render("! AetherCode"))
			b.WriteString("\n")
			if it.Severity == SeverityError {
				b.WriteString(styleError.Render(it.Text))
			} else {
				b.WriteString(it.Text)
			}
			b.WriteString("\n\n")
		}
	}
	return b.String()
}

// renderTurn writes one question-answer round. Streaming text shows complete
// lines only (the in-progress half-line stays hidden, so no flicker); once a
// part is sealed its whole markdown render is cached on the part itself.
func renderTurn(b *strings.Builder, m *Model, t *Turn) {
	b.WriteString(styleUserPrefix.Render("❯ You"))
	b.WriteString("\n")
	b.WriteString(t.Question)
	b.WriteString("\n\n")

	b.WriteString(styleAssistantPrefix.Render("● AetherCode"))
	b.WriteString("\n")
	for _, p := range t.Parts {
		switch p := p.(type) {
		case *TextPart:
			renderTextPart(b, m, p)
		case *ThinkingPart:
			// Thinking is stored but not rendered in this phase; its
			// collapsed display arrives with the agent-loop UI.
		case *ToolPart:
			// Tool calls are stored but not rendered in this phase; their
			// discrete block display arrives with the agent-loop UI.
		}
	}
	switch t.Status {
	case TurnStreaming:
		secs := int(timeSince(t.Started).Seconds())
		b.WriteString(styleDim.Render(fmt.Sprintf("Imagining… (%ds)", secs)))
	case TurnFailed:
		if t.Err != nil {
			b.WriteString(styleError.Render("Error: " + t.Err.Error()))
		} else {
			b.WriteString(styleError.Render("Error: request failed"))
		}
	case TurnDone:
		b.WriteString(styleDim.Render(fmt.Sprintf("%ds · done %s",
			int(t.Elapsed.Seconds()), t.DoneAt.Format("3:04 PM"))))
	}
	b.WriteString("\n\n")
}

// renderTextPart shows complete lines while streaming and the cached whole
// markdown render once sealed.
func renderTextPart(b *strings.Builder, m *Model, p *TextPart) {
	if !p.done {
		if vis := visibleLines(p.Text); vis != "" {
			b.WriteString(vis) // vis already ends with the last complete newline
		}
		return
	}
	out := p.cachedRender(m.width, func(md string) string {
		if m.renderer == nil {
			return md
		}
		out, err := m.renderer.Render(md)
		if err != nil {
			return md
		}
		return strings.Trim(out, "\n")
	})
	b.WriteString(out)
	b.WriteString("\n")
}

// visibleLines cuts streaming text at the last newline (inclusive) so the
// half-typed line never flashes on screen and complete lines appear one at a
// time. Text without a newline yet shows nothing.
func visibleLines(s string) string {
	i := strings.LastIndexByte(s, '\n')
	if i < 0 {
		return ""
	}
	return s[:i+1]
}

func (m *Model) renderInput() string {
	return styleInputBox.Width(max(m.width-2, 10)).Render(m.input.View())
}

func (m *Model) renderStatus() string {
	left := " " + m.opts.ProviderName
	right := m.opts.ModelName + " "
	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return styleStatusBar.Width(m.width).Render(left + strings.Repeat(" ", pad) + right)
}

// View implements tea.Model. It is a pure read of the current Model: all
// content was pushed to the viewport by followBottom during Update.
func (m Model) View() tea.View {
	content := lipgloss.JoinVertical(lipgloss.Left,
		renderBanner(m.opts.Version, m.opts.WorkDir, m.width),
		renderReady(),
		m.viewport.View(),
		m.renderInput(),
		m.renderStatus(),
	)
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
