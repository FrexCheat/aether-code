package tui

import (
	"time"

	"github.com/FrexCheat/aether-code/internal/tools"
)

// timeNow and timeSince are indirected so tests can freeze the clock.
var (
	timeNow   = time.Now
	timeSince = time.Since
)

// entryKind classifies a committed history entry for rendering.
type entryKind int

const (
	entryUser      entryKind = iota // user question
	entryAssistant                  // committed assistant reply
	entryNotice                     // system notice (incl. errors)
	entryTool                       // one tool call (running → settled)
	entryTurn                       // turn-level done footer (whole agent loop)
)

// toolState is the lifecycle of a tool entry between ToolCallBegin/End.
type toolState int

const (
	toolRunning toolState = iota
	toolDone
	toolFailed
)

// noticeSeverity grades a notice entry: plain info, yellow guard-rail warning
// (loop stopped by the turn cap or unknown-tool streak), or red error.
type noticeSeverity int

const (
	noticeInfo noticeSeverity = iota
	noticeWarn
	noticeError
)

// entry is one committed, displayable line of history. User, assistant and
// notice entries carry only text; tool entries carry the call's title, state,
// duration and diff; the turn entry closes one agent loop with its whole
// duration (the completion form of the live spinner timer).
type entry struct {
	kind     entryKind
	text     string
	severity noticeSeverity // notice grading
	elapsed  time.Duration
	doneAt   time.Time
	width    int    // markdown cache key
	rendered string // cached markdown render

	// Tool entry fields.
	toolID   string // model-issued call id; pairs Begin with End
	toolName string
	state    toolState
	diff     string // diff result the tool marked as one, line-numbered
}

func newUserEntry(text string) entry {
	return entry{kind: entryUser, text: text}
}

func newAssistantEntry(text string) entry {
	return entry{kind: entryAssistant, text: text}
}

// newTurnEntry closes one agent loop: elapsed spans the whole turn from the
// user's question to the terminal event, tool executions included.
func newTurnEntry(elapsed time.Duration, doneAt time.Time) entry {
	return entry{kind: entryTurn, elapsed: elapsed, doneAt: doneAt}
}

func newNoticeEntry(text string, severity noticeSeverity) entry {
	return entry{kind: entryNotice, text: text, severity: severity}
}

// newToolEntry opens a tool entry in running state; settleTool closes it.
// toolID is the model-issued call id: two calls to the same tool may be in
// flight at once, so name alone cannot pair Begin with End.
func newToolEntry(toolID, name, title string) entry {
	return entry{kind: entryTool, toolID: toolID, toolName: name, state: toolRunning, text: title}
}

func (p *entry) settleTool(failed bool, elapsed time.Duration, diff string) {
	p.elapsed = elapsed
	p.diff = diff
	if failed {
		p.state = toolFailed
	} else {
		p.state = toolDone
	}
}

// toolTitle summarizes one tool call's arguments into a human title by
// asking the tool itself (TitledTool), mewcode style: Read a.go / Write a.go
// (12 lines) / Edit a.go / Bash: <command> / Glob: pattern. Tools that don't
// implement the interface — and unregistered names — fall back to the plain
// tool name.
func toolTitle(reg *tools.Registry, name string, args map[string]any) string {
	if reg != nil {
		if t := reg.Get(name); t != nil {
			if tt, ok := t.(tools.TitledTool); ok {
				if title := tt.Title(args); title != "" {
					return title
				}
			}
		}
	}
	return name
}

// cachedRender returns the whole-text markdown render memoized per width. Only
// committed assistant entries are cached (they are final); a width change
// rebuilds the cache.
func (p *entry) cachedRender(width int, render func(string) string) string {
	if p.kind == entryAssistant && p.width == width && p.rendered != "" {
		return p.rendered
	}
	p.width = width
	p.rendered = render(p.text)
	return p.rendered
}
