package tools

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ReadFileTool returns the contents of one file, optionally paged by line
// offset and limit so large files can be read in chunks. Every successful
// read is recorded in the shared FileStateCache, which is what later lets
// edit_file and write_file verify the model has seen what it's changing.
type ReadFileTool struct {
	// WorkDir resolves relative path arguments (N3).
	WorkDir string
	// FileStateCache records read state for the edit gate; nil disables it.
	FileStateCache *FileStateCache
}

func (t *ReadFileTool) Name() string { return "read_file" }
func (t *ReadFileTool) Description() string {
	return ReadFileDescription
}
func (t *ReadFileTool) Category() Category { return CategoryRead }

func (t *ReadFileTool) Schema() map[string]any {
	return toolSchema(t.Name(), t.Description(), map[string]any{
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
	})
}

func (t *ReadFileTool) Execute(_ context.Context, args map[string]any) ToolResult {
	path, ok := argString(args, "file_path")
	if !ok {
		return errResult("read_file: missing required string argument \"file_path\"")
	}
	offset, err := argInt(args, "offset")
	if err != nil {
		return errResult("read_file: " + err.Error())
	}
	limit, err := argInt(args, "limit")
	if err != nil {
		return errResult("read_file: " + err.Error())
	}

	data, err := os.ReadFile(ResolveWorkPath(t.WorkDir, path))
	if err != nil {
		return errResult(fmt.Sprintf("read_file: cannot read %s: %v", path, err))
	}

	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	start := 0
	if offset > 0 {
		if offset > len(lines) {
			return errResult(fmt.Sprintf("read_file: offset %d beyond end of file (%d lines)", offset, len(lines)))
		}
		start = offset - 1
	}
	if limit <= 0 {
		limit = 2000
	}
	end := len(lines)
	if start+limit < end {
		end = start + limit
	}

	// Record the read for the edit-before-write gate.
	full := ResolveWorkPath(t.WorkDir, path)
	if t.FileStateCache != nil {
		if info, statErr := os.Stat(full); statErr == nil {
			t.FileStateCache.Record(full, info.ModTime().UnixMilli())
		}
	}

	var sb strings.Builder
	for i := start; i < end; i++ {
		fmt.Fprintf(&sb, "%6d\t%s\n", i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&sb, "... (%d more lines; use offset=%d to continue)\n", len(lines)-end, end+1)
	}
	return ToolResult{Output: TruncateOutput(strings.TrimSuffix(sb.String(), "\n"), MaxOutputChars)}
}

// argString fetches a required string argument (N2: validate before side effects).
func argString(args map[string]any, key string) (string, bool) {
	v, ok := args[key].(string)
	return v, ok && v != ""
}

// argInt fetches an optional integer argument. Absent is not an error; a
// present-but-non-integer value is.
func argInt(args map[string]any, key string) (int, error) {
	switch v := args[key].(type) {
	case nil:
		return 0, nil
	case float64:
		return int(v), nil
	case int:
		return v, nil
	case string:
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("argument %q must be an integer, got %q", key, v)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("argument %q must be an integer", key)
	}
}

// errResult is the shared constructor for structured failures.
func errResult(msg string) ToolResult {
	return ToolResult{Output: msg, IsError: true}
}
