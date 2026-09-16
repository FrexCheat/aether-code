package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EditFileTool replaces exactly one occurrence of old_string with new_string.
// Zero or multiple matches are refused without touching the file and the
// error says so, letting the model widen the context and retry. The file
// must have been read (and not changed on disk since) or the edit is refused.
type EditFileTool struct {
	WorkDir string
	// FileStateCache gates the edit on a prior read; nil disables the gate.
	FileStateCache *FileStateCache
}

func (t *EditFileTool) Name() string { return "edit_file" }
func (t *EditFileTool) Description() string {
	return EditFileDescription
}
func (t *EditFileTool) Category() Category { return CategoryWrite }

func (t *EditFileTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: t.Description(),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Path to the file, relative to the working directory or absolute",
				},
				"old_string": map[string]any{
					"type":        "string",
					"description": "Exact text to replace; must appear exactly once in the file",
				},
				"new_string": map[string]any{
					"type":        "string",
					"description": "Replacement text",
				},
			},
			"required": []string{"file_path", "old_string", "new_string"},
		},
	}
}

// Title summarizes the call for the UI: "Edit note.txt".
func (t *EditFileTool) Title(args map[string]any) string {
	if p, _ := args["file_path"].(string); p != "" {
		return "Edit " + filepath.Base(p)
	}
	return ""
}

func (t *EditFileTool) Execute(_ context.Context, args map[string]any) ToolResult {
	path, ok := argString(args, "file_path")
	if !ok {
		return errResult("edit_file: missing required string argument \"file_path\"")
	}
	oldStr, ok := args["old_string"].(string)
	if !ok {
		return errResult("edit_file: missing required string argument \"old_string\"")
	}
	newStr, ok := args["new_string"].(string)
	if !ok {
		return errResult("edit_file: missing required string argument \"new_string\"")
	}
	if oldStr == "" {
		return errResult("edit_file: \"old_string\" must not be empty")
	}

	full := ResolveWorkPath(t.WorkDir, path)
	if t.FileStateCache != nil {
		if ok, msg := t.FileStateCache.Check(full); !ok {
			return errResult("edit_file: " + msg)
		}
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return errResult(fmt.Sprintf("edit_file: cannot read %s: %v", path, err))
	}
	orig := string(data)

	switch n := strings.Count(orig, oldStr); {
	case n == 0:
		return errResult(fmt.Sprintf(
			"edit_file: old_string not found in %s; read the file again and use an exact excerpt including surrounding lines", path))
	case n > 1:
		return errResult(fmt.Sprintf(
			"edit_file: old_string appears %d times in %s; include more surrounding lines to make it unique", n, path))
	}

	updated := strings.Replace(orig, oldStr, newStr, 1)
	tmp, err := os.CreateTemp(filepath.Dir(full), "."+filepath.Base(full)+".tmp-*")
	if err != nil {
		return errResult(fmt.Sprintf("edit_file: cannot create temp file for %s: %v", path, err))
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.WriteString(updated); err != nil {
		tmp.Close()
		return errResult(fmt.Sprintf("edit_file: cannot write %s: %v", path, err))
	}
	if err := tmp.Close(); err != nil {
		return errResult(fmt.Sprintf("edit_file: cannot write %s: %v", path, err))
	}
	if err := os.Rename(tmpName, full); err != nil {
		return errResult(fmt.Sprintf("edit_file: cannot replace %s: %v", path, err))
	}
	if t.FileStateCache != nil {
		t.FileStateCache.Update(full)
	}
	// Report the concrete diff rather than a bare "done": both the model and
	// the TUI need to see which lines actually changed.
	diff := BuildDiff(orig, updated)
	summary := fmt.Sprintf(
		"Updated %s with %d addition%s and %d removal%s",
		path, diff.Additions, plural(diff.Additions), diff.Removals, plural(diff.Removals))
	return ToolResult{
		Output: TruncateOutput(summary+"\n"+diff.Text, MaxOutputChars),
		Diff:   true,
	}
}

// DiffResult is BuildDiff's return: the rendered diff text plus the number
// of added and removed lines.
type DiffResult struct {
	Text      string
	Additions int
	Removals  int
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// BuildDiff renders a line-oriented diff between the old and new file content
// in the text form the TUI colors: "- " removed lines, "+ " added lines,
// "  " context lines. The changed region carries three lines of context on
// each side, enough for the model to recognize the edit site.
func BuildDiff(oldStr, newStr string) DiffResult {
	oldLines := strings.Split(strings.TrimSuffix(oldStr, "\n"), "\n")
	newLines := strings.Split(strings.TrimSuffix(newStr, "\n"), "\n")

	// Longest common prefix/suffix line counts around the single edit site.
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}

	const ctxLines = 3
	ctxStart := max(0, prefix-ctxLines)
	ctxEnd := min(len(oldLines), len(oldLines)-suffix+ctxLines)

	result := DiffResult{
		Additions: len(newLines) - prefix - suffix,
		Removals:  len(oldLines) - prefix - suffix,
	}

	var sb strings.Builder
	oldNo, newNo := ctxStart+1, ctxStart+1
	line := func(marker string, no int, text string) {
		fmt.Fprintf(&sb, "%s %3d  %s\n", marker, no, text)
	}
	for _, l := range oldLines[ctxStart:prefix] {
		line(" ", oldNo, l)
		oldNo++
		newNo++
	}
	for _, l := range oldLines[prefix : len(oldLines)-suffix] {
		line("-", oldNo, l)
		oldNo++
	}
	for _, l := range newLines[prefix : len(newLines)-suffix] {
		line("+", newNo, l)
		newNo++
	}
	for _, l := range oldLines[len(oldLines)-suffix : ctxEnd] {
		line(" ", oldNo, l)
		oldNo++
		newNo++
	}
	result.Text = strings.TrimSuffix(sb.String(), "\n")
	return result
}
