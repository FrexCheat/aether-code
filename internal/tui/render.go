package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/format"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/session"
	"github.com/FrexCheat/aether-code/internal/tools"
	"github.com/FrexCheat/aether-code/internal/tui/components"
)

const (
	collapseLines   = 3
	titleClampChars = 160

	// bannerHint is the banner's one-line hint. It deliberately does not
	// enumerate the commands: with seven of them the hint outgrew the space
	// beside the logo, which the banner then silently dropped. Tab completion
	// and /help are the discovery path.
	bannerHint = "Press Enter to send · shift+enter for a newline · / for commands"

	asciiLogo = `
 ┌╌╌╌╌╌┐
 └╌╌╌╌╌┘
    /▞▚\
   /▟█▙ \
▅▅▅▅    ▅▅▅
`
)

type noticeSeverity int

const (
	noticeInfo noticeSeverity = iota
	noticeWarn
	noticeError
)

// blockKind selects the scrollback rendering of a block. Assistant text,
// tool results and notices are the only things that reach the transcript.
type blockKind int

const (
	blockUser blockKind = iota
	blockAssistant
	blockTool
	blockTurn
	blockNotice
	blockStatus
)

// block is one printed unit of history. It carries the full, uncollapsed
// content, so the same record renders both the collapsed scrollback line and
// the expanded ctrl+o view.
type block struct {
	kind     blockKind
	title    string
	body     string
	elapsed  time.Duration
	at       time.Time
	usage    llm.Usage
	isErr    bool
	isDiff   bool
	severity noticeSeverity
}

// blockRenderer turns a block into text. It is the only place a block reaches
// the terminal, so scrollback and the expanded view can never diverge in style.
type blockRenderer struct {
	width    int
	markdown *glamour.TermRenderer
}

// SetWidth rebuilds the markdown renderer against a new terminal width.
func (r *blockRenderer) SetWidth(w int) {
	r.width = w
	if w <= 0 {
		r.markdown = nil
		return
	}
	sc := styles.DarkStyleConfig
	var zeroMargin uint
	sc.Document.Margin = &zeroMargin
	sc.Document.BlockPrefix = ""
	sc.Document.BlockSuffix = ""
	if markdown, err := glamour.NewTermRenderer(glamour.WithStyles(sc), glamour.WithWordWrap(max(w-2, 20))); err == nil {
		r.markdown = markdown
	}
}

func (r blockRenderer) Render(b block, expanded bool) string {
	switch b.kind {
	case blockUser:
		return components.UserBubble.Width(max(r.width, 10)).Render(b.body)
	case blockAssistant:
		return r.assistant(b.body)
	case blockTool:
		return r.tool(b, expanded)
	case blockTurn:
		return components.Dim.Render(turnLine(b.elapsed, b.at, b.usage))
	case blockStatus:
		return components.Dim.Render(b.body)
	case blockNotice:
		return renderNotice(b.body, b.severity)
	}
	return b.body
}

func (r blockRenderer) assistant(text string) string {
	out := text
	if r.markdown != nil {
		if rendered, err := r.markdown.Render(text); err == nil {
			out = strings.Trim(rendered, "\n")
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, components.AssistantDot.Render("●")+" ", out)
}

func (r blockRenderer) tool(b block, expanded bool) string {
	dot := components.ToolDone.Render("●")
	if b.isErr {
		dot = components.ErrorText.Render("●")
	}
	head := b.title
	if b.elapsed > 0 {
		head += components.Dim.Render(fmt.Sprintf(" (%.1fs)", b.elapsed.Seconds()))
	}
	if b.body != "" {
		head += "\n" + r.toolBody(b, expanded)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, components.ToolIndent+dot+" ", head)
}

// toolBody renders the result text: collapsed to a few lines in the
// scrollback, the whole wrapped body in the expanded view.
func (r blockRenderer) toolBody(b block, expanded bool) string {
	lines := components.WrapLines(b.body, max(r.width-6, 20))
	if !expanded && len(lines) > collapseLines+1 {
		hint := components.Dim.Render(fmt.Sprintf("… +%d lines (ctrl+o to expand)", len(lines)-collapseLines))
		lines = append(lines[:collapseLines], hint)
	}
	for i, line := range lines {
		lines[i] = colorToolLine(line, b.isDiff, b.isErr)
	}
	return strings.Join(lines, "\n")
}

// Banner draws the startup banner. The model name sits with the rest of the
// banner text rather than being right-aligned against the margin, where a
// narrow terminal would clip it; the provider name is deliberately absent.
func (r blockRenderer) Banner(version, workDir, model, hint string) string {
	lines := []string{
		components.Title.Render("Aether Code") + " " + components.Dim.Render("v"+version),
		components.Dim.Render(workDir),
	}
	if model != "" {
		lines = append(lines, components.Dim.Render(model))
	}
	lines = append(lines, components.Dim.Render(hint))
	text := lipgloss.JoinVertical(lipgloss.Left, lines...)

	logo := components.Logo.Render(asciiLogo)
	if r.width > 0 && lipgloss.Width(logo)+3+lipgloss.Width(text) > r.width {
		return text
	}
	return lipgloss.JoinHorizontal(lipgloss.Center, logo, "   ", text)
}

// historyBlocks rebuilds the scrollback from a restored history: user and
// assistant text verbatim, tool calls paired with their results in record
// order, compaction boundaries as notices. Reminders and thinking never reach
// the archive, so there is nothing to skip, and per-turn timing is not
// recorded — elapsed stays zero, which the renderer drops.
func historyBlocks(msgs []conversation.Message, sess *session.Session, reg *tools.Registry) []block {
	results := make(map[string]conversation.ToolResultBlock)
	for _, m := range msgs {
		for _, tr := range m.ToolResults {
			results[tr.ToolUseID] = tr
		}
	}
	var blocks []block
	for _, m := range msgs {
		if sess != nil && sess.IsBoundary(m.Content) {
			blocks = append(blocks, block{kind: blockNotice, body: "Context compacted — earlier conversation summarized."})
			continue
		}
		switch m.Role {
		case conversation.RoleUser:
			if m.Content != "" {
				blocks = append(blocks, block{kind: blockUser, body: m.Content})
			}
		case conversation.RoleAssistant:
			if m.Content != "" {
				blocks = append(blocks, block{kind: blockAssistant, body: m.Content})
			}
			for _, tu := range m.ToolUses {
				b := block{kind: blockTool, title: toolTitle(reg, tu.Name, tu.Args)}
				if tr, ok := results[tu.ID]; ok {
					b.body = tr.Content
					b.isErr = tr.IsError
				}
				blocks = append(blocks, b)
			}
		}
	}
	return blocks
}

// toolTitle is the one place tool titles get trimmed for display: one line,
// titleClampChars runes. The clamp is a pure display concern.
func toolTitle(reg *tools.Registry, name string, args map[string]any) string {
	title := name
	if reg != nil {
		if t := reg.Get(name); t != nil {
			if tt, ok := t.(tools.TitledTool); ok {
				if s := tt.Title(args); s != "" {
					title = s
				}
			}
		}
	}
	title = strings.Join(strings.Fields(title), " ")
	if runes := []rune(title); len(runes) > titleClampChars {
		return string(runes[:titleClampChars]) + "…"
	}
	return title
}

func renderNotice(text string, severity noticeSeverity) string {
	var b strings.Builder
	b.WriteString(components.Notice.Render("! AetherCode"))
	b.WriteString("\n")
	switch severity {
	case noticeError:
		b.WriteString(components.ErrorText.Render(text))
	case noticeWarn:
		b.WriteString(components.WarnText.Render(text))
	default:
		b.WriteString(text)
	}
	return b.String()
}

// turnLine reports the cache split of the turn's last request — the prompt the
// model actually finished on — rather than a sum over the turn's rounds.
func turnLine(elapsed time.Duration, at time.Time, usage llm.Usage) string {
	line := fmt.Sprintf("%ds · done %s · Cache Read %s tokens",
		int(elapsed.Seconds()), at.Format("3:04 PM"),
		format.Tokens(usage.CacheReadTokens))
	if usage.CacheCreationTokens > 0 {
		line += fmt.Sprintf(" · Cache Create %s tokens", format.Tokens(usage.CacheCreationTokens))
	}
	return line
}

func colorToolLine(line string, isDiff, isErr bool) string {
	switch {
	case isDiff:
		return colorDiffLine(line)
	case isErr:
		return components.ErrorText.Render(line)
	}
	return line
}

func colorDiffLine(line string) string {
	switch {
	case strings.HasPrefix(line, "+"):
		return components.DiffAdd.Render(line)
	case strings.HasPrefix(line, "-"):
		return components.DiffRemove.Render(line)
	}
	return components.Dim.Render(line)
}

// hardWrap clamps every line to the terminal width, so a rendered block always
// occupies exactly the rows it reports.
func hardWrap(s string, width int) []string {
	if width <= 0 {
		width = 80
	}
	return components.WrapLines(strings.ReplaceAll(s, "\r\n", "\n"), width)
}
