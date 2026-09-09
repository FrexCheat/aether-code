package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// asciiLogo is a block-character rendering of the AetherCode mark in logo.txt:
// a stylised "A" with a detached top bar, a long left leg, a crossbar and a
// foot at the bottom right.
const asciiLogo = `
      ┌╌╌╌╌▗
      └╌╌╌╌╌▘

        /▞  ▚\
       /▞    ▚\
      /▞
     /▟████████▙
    /▟█
▅▅▅▅▅▅▅▅     ▅▅▅▅▅▅▅▅
`
const readyText = "Ready. Type a message and press Enter. /quit to exit."

// renderBanner draws the startup banner: logo on the left, application name,
// version and working directory on the right. If the terminal is too narrow
// for the logo, only the text is shown.
func renderBanner(version, workDir string, width int) string {
	text := lipgloss.JoinVertical(lipgloss.Left,
		styleTitle.Render("AetherCode")+" "+styleDim.Render("v"+version),
		styleDim.Render(workDir),
	)
	logo := styleBanner.Render(asciiLogo)
	full := lipgloss.JoinHorizontal(lipgloss.Center, logo, "   ", text)
	if width > 0 && lipgloss.Width(full) > width {
		return text
	}
	return full
}

func renderReady() string {
	return styleReady.Render(readyText)
}

// bannerHeight is the number of lines the banner plus the ready line occupy.
func bannerHeight(version, workDir string, width int) int {
	return lipgloss.Height(renderBanner(version, workDir, width)) + strings.Count(readyText, "\n") + 1
}
