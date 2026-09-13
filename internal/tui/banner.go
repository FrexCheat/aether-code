package tui

import (
	"charm.land/lipgloss/v2"
)

const asciiLogo = `
 ┌╌╌╌╌╌┐
 └╌╌╌╌╌┘
    /▞▚\
   /▟█▙ \
▅▅▅▅    ▅▅▅
`
const tipsText = "Press Enter to send · /clear /quit"

func renderBanner(version, workDir string, width int) string {
	text := lipgloss.JoinVertical(lipgloss.Left,
		styleTitle.Render("AetherCode")+" "+styleDim.Render("v"+version),
		styleDim.Render(workDir),
		styleTips.Render(tipsText),
	)
	logo := styleBanner.Render(asciiLogo)
	full := lipgloss.JoinHorizontal(lipgloss.Center, logo, "   ", text)
	if width > 0 && lipgloss.Width(full) > width {
		return text
	}
	return full
}

func renderStartup(version, workDir string, width int) string {
	return renderBanner(version, workDir, width)
}
