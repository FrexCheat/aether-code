package tui

import "time"

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
)

// entry is one committed, displayable line of history. User and notice entries
// carry only text; assistant entries additionally carry the timing used for
// the message-bottom footer (the completion form of the streaming timer).
type entry struct {
	kind     entryKind
	text     string
	isError  bool // notice error severity
	elapsed  time.Duration
	doneAt   time.Time
	width    int    // markdown cache key
	rendered string // cached markdown render
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
