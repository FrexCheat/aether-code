package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
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
)

// toolState is the lifecycle of a tool entry between ToolCallBegin/End.
type toolState int

const (
	toolRunning toolState = iota
	toolDone
	toolFailed
)

// entry is one committed, displayable line of history. User and notice entries
// carry only text; assistant entries additionally carry the timing used for
// the message-bottom footer (the completion form of the streaming timer); tool
// entries carry the call's title, state, duration and edit diff.
type entry struct {
	kind     entryKind
	text     string
	isError  bool // notice error severity
	elapsed  time.Duration
	doneAt   time.Time
	width    int    // markdown cache key
	rendered string // cached markdown render

	// Tool entry fields.
	toolName string
	state    toolState
	diff     string // edit_file result: line-numbered diff text
}

func newUserEntry(text string) entry {
	return entry{kind: entryUser, text: text}
}

func newAssistantEntry(text string, elapsed time.Duration, doneAt time.Time) entry {
	return entry{kind: entryAssistant, text: text, elapsed: elapsed, doneAt: doneAt}
}

func newNoticeEntry(text string, isError bool) entry {
	return entry{kind: entryNotice, text: text, isError: isError}
}

// newToolEntry opens a tool entry in running state; settleTool closes it.
func newToolEntry(name, title string) entry {
	return entry{kind: entryTool, toolName: name, state: toolRunning, text: title}
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

// toolTitle summarizes one tool call's arguments into a human title, mewcode
// style: Read a.go / Write a.go (12 lines) / Edit a.go / Bash: <50 chars> /
// Glob: pattern / Grep: pattern.
func toolTitle(name string, args map[string]any) string {
	str := func(key string) string { s, _ := args[key].(string); return s }
	switch name {
	case "read_file":
		if p := str("file_path"); p != "" {
			return "Read " + filepath.Base(p)
		}
	case "write_file":
		p := str("file_path")
		content, _ := args["content"].(string)
		lines := strings.Count(content, "\n") + 1
		if p != "" {
			return fmt.Sprintf("Write %s (%d lines)", filepath.Base(p), lines)
		}
	case "edit_file":
		if p := str("file_path"); p != "" {
			return "Edit " + filepath.Base(p)
		}
	case "bash":
		cmd := str("command")
		if len(cmd) > 50 {
			cmd = cmd[:50] + "…"
		}
		if cmd != "" {
			return "Bash: " + cmd
		}
	case "glob":
		if pattern := str("pattern"); pattern != "" {
			return "Glob: " + pattern
		}
	case "grep":
		if pattern := str("pattern"); pattern != "" {
			return "Grep: " + pattern
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
