package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
)

const (
	inputBoxHeight  = 3 // border top + content + border bottom
	statusBarHeight = 1
)

// layout distributes the terminal height between the fixed regions and the
// chat viewport, and rebuilds the markdown renderer for the new width.
func (m *Model) layout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	chatH := m.height - bannerHeight(m.opts.Version, m.opts.WorkDir, m.width) - inputBoxHeight - statusBarHeight
	if chatH < 1 {
		chatH = 1
	}
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(chatH)
	m.input.SetWidth(max(m.width-6, 10)) // border 2 + padding 2 + prompt slack

	wrap := max(m.width-2, 20)
	if r, err := glamour.NewTermRenderer(glamour.WithStylePath("dark"), glamour.WithWordWrap(wrap)); err == nil {
		m.renderer = r
	}
}

// refresh pushes the current chat content into the viewport and follows the bottom.
func (m *Model) refresh() {
	m.viewport.SetContent(m.renderChat())
	m.viewport.GotoBottom()
}

func (m Model) renderChat() string {
	var b strings.Builder
	for _, e := range m.entries {
		switch e.role {
		case "user":
			b.WriteString(styleUserPrefix.Render("❯ You"))
			b.WriteString("\n")
			b.WriteString(e.text)
			b.WriteString("\n\n")
		default:
			b.WriteString(styleAssistantPrefix.Render("● AetherCode"))
			b.WriteString("\n")
			switch {
			case e.err != nil:
				b.WriteString(styleError.Render("Error: " + e.err.Error()))
			case !e.done:
				if e.text != "" {
					b.WriteString(e.text)
					b.WriteString("\n")
				}
				secs := int(timeSince(e.started).Seconds())
				b.WriteString(styleDim.Render(fmt.Sprintf("Imagining… (%ds)", secs)))
			default:
				b.WriteString(e.rendered)
				b.WriteString("\n")
				b.WriteString(styleDim.Render(fmt.Sprintf("(%ds)", int(e.elapsed.Seconds()))))
			}
			b.WriteString("\n\n")
		}
	}
	return b.String()
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

// View implements tea.Model.
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
