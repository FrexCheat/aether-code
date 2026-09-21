package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ReadFileTool struct {
	WorkDir        string
	FileStateCache *FileStateCache
}

func (t *ReadFileTool) Name() string { return "Read" }
func (t *ReadFileTool) Description() string {
	return ReadFileDescription
}
func (t *ReadFileTool) Category() Category { return CategoryRead }

func (t *ReadFileTool) Schema() ToolSchema {
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
				"offset": map[string]any{
					"type":        "integer",
					"description": "1-based line number to start reading from",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of lines to read (default 2000)",
				},
			},
			"required": []string{"file_path"},
		},
	}
}

func (t *ReadFileTool) Title(args map[string]any) string {
	if p, _ := args["file_path"].(string); p != "" {
		return "Read " + filepath.Base(p)
	}
	return ""
}

func (t *ReadFileTool) Execute(_ context.Context, args map[string]any) ToolResult {
	path, ok := argString(args, "file_path")
	if !ok {
		return errResult("Read: missing required string argument \"file_path\"")
	}
	offset, err := argInt(args, "offset")
	if err != nil {
		return errResult("Read: " + err.Error())
	}
	limit, err := argInt(args, "limit")
	if err != nil {
		return errResult("Read: " + err.Error())
	}

	data, err := os.ReadFile(ResolveWorkPath(t.WorkDir, path))
	if err != nil {
		return errResult(fmt.Sprintf("Read: cannot read %s: %v", path, err))
	}

	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	start := 0
	if offset > 0 {
		if offset > len(lines) {
			return errResult(fmt.Sprintf("Read: offset %d beyond end of file (%d lines)", offset, len(lines)))
		}
		start = offset - 1
	}
	if limit <= 0 {
		limit = 2000
	}
	end := min(start+limit, len(lines))

	// Record the read for the edit-before-write gate.
	full := ResolveWorkPath(t.WorkDir, path)
	if t.FileStateCache != nil {
		t.FileStateCache.Record(full)
	}

	var sb strings.Builder
	for i := start; i < end; i++ {
		fmt.Fprintf(&sb, "%6d\t%s\n", i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&sb, "... (%d more lines; use offset=%d to continue)\n", len(lines)-end, end+1)
	}
	return ToolResult{Output: strings.TrimSuffix(sb.String(), "\n")}
}
