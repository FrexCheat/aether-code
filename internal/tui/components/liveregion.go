package components

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/FrexCheat/aether-code/internal/agent"
)

const maxRunningTools = 5

type toolCall struct {
	id    string
	title string
}

// LiveRegion renders the turn in flight — the running tools, the streamed
// assistant text and the activity line — as the tail of the history. Its groups
// are separated like history blocks, so the tail keeps its shape once flushed.
// The caller supplies the clock's frame, so no row owns a tick chain.
type LiveRegion struct {
	events       <-chan agent.Event
	cancel       func()
	text         []byte
	tools        []toolCall
	toolSpinner  Spinner
	labelSpinner Spinner
	timer        Stopwatch
	width        int
	wrapped      []string // streamed lines already wrapped at the current width
	partial      string   // the trailing line, still growing
}

// NewLiveRegion builds the region with the spinner each animated row shows.
func NewLiveRegion(toolSpinner, labelSpinner Spinner) LiveRegion {
	return LiveRegion{toolSpinner: toolSpinner, labelSpinner: labelSpinner}
}

func (l LiveRegion) Active() bool { return l.cancel != nil }

func (l LiveRegion) Events() <-chan agent.Event { return l.events }

func (l LiveRegion) Elapsed() time.Duration { return l.timer.Elapsed() }

func (l *LiveRegion) Begin(events <-chan agent.Event, cancel func()) {
	l.events, l.cancel = events, cancel
	l.reset()
	l.timer.Start()
}

func (l *LiveRegion) Finish() {
	l.events, l.cancel = nil, nil
	l.reset()
}

// reset drops the turn's streamed text and running tools.
func (l *LiveRegion) reset() {
	l.text = nil
	l.tools = nil
	l.wrapped, l.partial = nil, ""
}

func (l *LiveRegion) Cancel() {
	if l.cancel != nil {
		l.cancel()
	}
}

func (l *LiveRegion) Pause()  { l.timer.Pause() }
func (l *LiveRegion) Resume() { l.timer.Resume() }

func (l *LiveRegion) SetWidth(w int) {
	if l.width == w {
		return
	}
	l.width = w
	l.wrapped, l.partial = nil, ""
	l.appendText(string(l.text))
}

func (l *LiveRegion) AppendText(s string) {
	if !l.Active() {
		return
	}
	l.text = append(l.text, s...)
	l.appendText(s)
}

func (l *LiveRegion) BeginTool(id, title string) {
	if l.Active() {
		l.tools = append(l.tools, toolCall{id: id, title: title})
	}
}

// EndTool stops showing a running tool and reports the title it was shown under.
func (l *LiveRegion) EndTool(id string) (title string, found bool) {
	for i, call := range l.tools {
		if call.id == id {
			l.tools = slices.Delete(l.tools, i, i+1)
			return call.title, true
		}
	}
	return "", false
}

// TakeText returns and clears the streamed text, so it flushes exactly once.
func (l *LiveRegion) TakeText() string {
	text := string(l.text)
	l.text = nil
	l.wrapped, l.partial = nil, ""
	return text
}

// Lines is the live tail of the history: the running tools, the streamed text
// in full, and the activity line. Every group is followed by a blank line — the
// same separator its history blocks carry — so the tail does not look jammed.
func (l LiveRegion) Lines(tick int) []string {
	if !l.Active() {
		return nil
	}
	var lines []string
	if tools := l.toolLines(tick); len(tools) > 0 {
		lines = append(lines, tools...)
		lines = append(lines, "")
	}
	if text := l.textLines(); len(text) > 0 {
		lines = append(lines, text...)
		lines = append(lines, "")
	}
	label := fmt.Sprintf("Imagining… (%ds)", int(l.timer.Elapsed().Seconds()))
	return append(lines, l.labelSpinner.Line(tick, label))
}

// appendText folds new text into the wrapped-line cache: everything before the
// last newline is complete and wrapped once, while the trailing line stays
// partial so a delta never re-wraps what is already done.
func (l *LiveRegion) appendText(s string) {
	segments := strings.Split(l.partial+s, "\n")
	l.partial = segments[len(segments)-1]
	for _, line := range segments[:len(segments)-1] {
		l.wrapped = append(l.wrapped, WrapLines(line, max(l.width, 20))...)
	}
}

// toolLines is the running-tool group: the tools still executing, capped with a
// "+N more running" line.
func (l LiveRegion) toolLines(tick int) []string {
	if len(l.tools) == 0 {
		return nil
	}
	if len(l.tools) > maxRunningTools {
		lines := make([]string, 0, maxRunningTools)
		for _, call := range l.tools[:maxRunningTools-1] {
			lines = append(lines, l.runningLine(call, tick))
		}
		more := fmt.Sprintf("+%d more running", len(l.tools)-(maxRunningTools-1))
		return append(lines, ToolIndent+Dim.Render(more))
	}
	lines := make([]string, 0, len(l.tools))
	for _, call := range l.tools {
		lines = append(lines, l.runningLine(call, tick))
	}
	return lines
}

// textLines is the streamed assistant text, wrapped.
func (l LiveRegion) textLines() []string {
	if l.partial == "" {
		return l.wrapped
	}
	return append(slices.Clone(l.wrapped), WrapLines(l.partial, max(l.width, 20))...)
}

func (l LiveRegion) runningLine(call toolCall, tick int) string {
	return ToolIndent + l.toolSpinner.Frame(tick) + " " + call.title
}
