package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"

	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/permissions"
)

var (
	styleUserBubble   = lipgloss.NewStyle().Background(lipgloss.Color("8")).Padding(0, 1)
	styleAssistantDot = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	styleDim          = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleError        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	styleWarn         = lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // guard-rail notices (turn limit, unknown tool)
	styleInputBox     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
	styleStatusBar    = lipgloss.NewStyle().Reverse(true)
	styleNoticePrefix = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	styleBanner       = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	styleTitle        = lipgloss.NewStyle().Bold(true)
	styleTips         = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleSpinner      = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))

	styleToolDone   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleToolFailed = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	styleDiffAdd    = lipgloss.NewStyle().Background(lipgloss.Color("2"))
	styleDiffRemove = lipgloss.NewStyle().Background(lipgloss.Color("1"))

	// Permission dialog: the yellow reserved for things that need attention,
	// with the selected answer carrying the same color as its border.
	stylePermBox      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("3")).Padding(0, 1)
	stylePermTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	stylePermTool     = lipgloss.NewStyle().Bold(true)
	stylePermCursor   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	stylePermSelected = lipgloss.NewStyle().Bold(true)
	stylePermDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

func inputStyles() textarea.Styles {
	clean := lipgloss.NewStyle()
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	selection := lipgloss.NewStyle().Background(lipgloss.Color("8"))
	return textarea.Styles{
		Focused: textarea.StyleState{
			Base:        clean,
			Text:        clean,
			CursorLine:  clean,
			EndOfBuffer: dim,
			Placeholder: dim,
			Prompt:      clean,
			Selection:   selection,
		},
		Blurred: textarea.StyleState{
			Base:        clean,
			Text:        dim,
			CursorLine:  clean,
			EndOfBuffer: dim,
			Placeholder: dim,
			Prompt:      clean,
			Selection:   selection,
		},
		Cursor: textarea.CursorStyle{
			Color: lipgloss.Color("7"),
			Blink: true,
		},
	}
}

const toolIndent = "  "

type noticeSeverity int

const (
	noticeInfo noticeSeverity = iota
	noticeWarn
	noticeError
)

type permissionOption struct {
	label    string
	response permissions.Response
}

var permissionOptions = []permissionOption{
	{label: "Allow once", response: permissions.AllowOnce},
	{label: "Allow for this session", response: permissions.AllowSession},
	{label: "Always allow", response: permissions.AllowPermanent},
	{label: "Deny", response: permissions.Reject},
}

// permissionOptionsFor drops the two options that would record a rule when the
// call has none to record. A rule cannot cover every command, and a grant that
// silently records nothing would ask the same question again next time.
func permissionOptionsFor(req *permissions.Request) []permissionOption {
	if len(req.Rules) == 0 {
		return []permissionOption{
			{label: "Allow once", response: permissions.AllowOnce},
			{label: "Deny", response: permissions.Reject},
		}
	}
	return permissionOptions
}

type components struct {
	width    int
	spinner  string
	markdown *glamour.TermRenderer
}

func (m *Model) components() components {
	return components{width: m.width, spinner: m.spinner.View(), markdown: m.renderer}
}

const (
	asciiLogo = `
 ┌╌╌╌╌╌┐
 └╌╌╌╌╌┘
    /▞▚\
   /▟█▙ \
▅▅▅▅    ▅▅▅
`
	tipsText = "Press Enter to send · /clear /quit"
)

func formatTokenCount(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	k := strconv.FormatFloat(float64(n)/1000, 'f', 1, 64)
	return strings.TrimSuffix(k, ".0") + "k"
}

func (c components) banner(version, workDir string) string {
	text := lipgloss.JoinVertical(lipgloss.Left,
		styleTitle.Render("Aether Code")+" "+styleDim.Render("v"+version),
		styleDim.Render(workDir),
		styleTips.Render(tipsText),
	)
	full := lipgloss.JoinHorizontal(lipgloss.Center, styleBanner.Render(asciiLogo), "   ", text)
	if c.width > 0 && lipgloss.Width(full) > c.width {
		return text
	}
	return full
}

func (c components) notice(text string, severity noticeSeverity) string {
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

func (c components) user(text string) string {
	return styleUserBubble.Width(c.width).Render(text)
}

func (c components) assistant(text string) string {
	out := text
	if c.markdown != nil {
		if r, err := c.markdown.Render(text); err == nil {
			out = strings.Trim(r, "\n")
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, styleAssistantDot.Render("●")+" ", out)
}

func (c components) live(secs int) string {
	return styleSpinner.Render(c.spinner + fmt.Sprintf(" Imagining… (%ds)", secs))
}

func (c components) turn(elapsed time.Duration, doneAt time.Time, usage llm.Usage) string {
	total := usage.InputTokens + usage.OutputTokens + usage.CacheReadTokens + usage.CacheCreationTokens
	line := fmt.Sprintf("%ds · done %s · ↓ %s tokens",
		int(elapsed.Seconds()), doneAt.Format("3:04 PM"), formatTokenCount(total))
	if usage.CacheReadTokens > 0 {
		line += " · cache " + formatTokenCount(usage.CacheReadTokens)
	}
	return styleDim.Render(line)
}

func (c components) input(view string) string {
	return styleInputBox.Width(max(c.width-2, 10)).Render(view)
}

func (c components) status(provider, mode, model string) string {
	left := " " + provider
	if mode != "" {
		left += " · " + mode
	}
	right := model + " "
	pad := max(1, c.width-lipgloss.Width(left)-lipgloss.Width(right))
	return styleStatusBar.Width(c.width).Render(left + strings.Repeat(" ", pad) + right)
}

func (c components) cleared() string {
	return styleDim.Render("— context cleared —")
}

func (c components) toolRunning(title string) string {
	return toolIndent + styleSpinner.Render(c.spinner) + " " + title
}

func (c components) toolDone(title string, failed bool, elapsed time.Duration, diff string) string {
	dot := styleToolDone.Render("●")
	if failed {
		dot = styleToolFailed.Render("●")
	}
	var body strings.Builder
	body.WriteString(title)
	body.WriteString(styleDim.Render(fmt.Sprintf(" (%.1fs)", elapsed.Seconds())))
	if diff != "" {
		for line := range strings.SplitSeq(diff, "\n") {
			body.WriteString("\n")
			switch {
			case strings.HasPrefix(line, "+"):
				body.WriteString(styleDiffAdd.Render(line))
			case strings.HasPrefix(line, "-"):
				body.WriteString(styleDiffRemove.Render(line))
			default:
				body.WriteString(styleDim.Render(line))
			}
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, toolIndent+dot+" ", body.String())
}

func (c components) permissionPrompt(req *permissions.Request, cursor int) string {
	boxWidth := max(c.width-2, 20)
	inner := max(boxWidth-2, 10) // box width minus its padding; the border sits outside

	var parts []string
	parts = append(parts, stylePermTitle.Render("Permission required"), "")
	heading := stylePermTool.Render(req.ToolName)
	if req.Action != "" {
		heading += "\n" + lipgloss.Wrap(req.Action, inner, " -/")
	}
	parts = append(parts, heading)

	if len(req.Rules) > 0 {
		lines := []string{"", stylePermDim.Render("Granting it would record:")}
		for _, rule := range req.Rules {
			lines = append(lines, stylePermDim.Render(lipgloss.Wrap("  • "+rule.String(), inner, " -/")))
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}

	choices := permissionOptionsFor(req)
	options := make([]string, 0, len(choices))
	for i, opt := range choices {
		if i == cursor {
			options = append(options, stylePermCursor.Render("› ")+stylePermSelected.Render(opt.label))
			continue
		}
		options = append(options, "  "+opt.label)
	}
	parts = append(parts, strings.Join(options, "\n"), "")
	parts = append(parts, stylePermDim.Render("↑/↓ to move · Enter to confirm · Esc to deny"))

	return stylePermBox.Width(boxWidth).Render(strings.Join(parts, "\n"))
}
