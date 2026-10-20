package components

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

const maxRunningTools = 5

type toolCall struct {
	id    string
	title string
}

type LiveRegion struct {
	running      bool
	text         []byte
	tools        []toolCall
	toolSpinner  Spinner
	labelSpinner Spinner
	timer        Stopwatch
	width        int
	wrapped      []string // streamed lines already wrapped at the current width
	partial      string   // the trailing line, still growing
}

func NewLiveRegion(toolSpinner, labelSpinner Spinner) LiveRegion {
	return LiveRegion{toolSpinner: toolSpinner, labelSpinner: labelSpinner}
}

func (l LiveRegion) Active() bool { return l.running }

func (l LiveRegion) Elapsed() time.Duration { return l.timer.Elapsed() }

func (l *LiveRegion) Begin() {
	l.running = true
	l.reset()
	l.timer.Start()
}

func (l *LiveRegion) Finish() {
	l.running = false
	l.reset()
}

func (l *LiveRegion) reset() {
	l.text = nil
	l.tools = nil
	l.wrapped, l.partial = nil, ""
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

func (l *LiveRegion) EndTool(id string) (title string, found bool) {
	for i, call := range l.tools {
		if call.id == id {
			l.tools = slices.Delete(l.tools, i, i+1)
			return call.title, true
		}
	}
	return "", false
}

func (l *LiveRegion) TakeText() string {
	text := string(l.text)
	l.text = nil
	l.wrapped, l.partial = nil, ""
	return text
}

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

// Only complete lines enter the wrapped cache; the trailing line keeps growing.
func (l *LiveRegion) appendText(s string) {
	segments := strings.Split(l.partial+s, "\n")
	l.partial = segments[len(segments)-1]
	for _, line := range segments[:len(segments)-1] {
		l.wrapped = append(l.wrapped, WrapLines(line, max(l.width, 20))...)
	}
}

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

func (l LiveRegion) textLines() []string {
	if l.partial == "" {
		return l.wrapped
	}
	return append(slices.Clone(l.wrapped), WrapLines(l.partial, max(l.width, 20))...)
}

func (l LiveRegion) runningLine(call toolCall, tick int) string {
	return ToolIndent + l.toolSpinner.Frame(tick) + " " + call.title
}
