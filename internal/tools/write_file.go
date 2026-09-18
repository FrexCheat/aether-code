package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type WriteFileTool struct {
	WorkDir        string
	FileStateCache *FileStateCache
}

func (t *WriteFileTool) Name() string { return "Write" }
func (t *WriteFileTool) Description() string {
	return WriteFileDescription
}
func (t *WriteFileTool) Category() Category { return CategoryWrite }

func (t *WriteFileTool) Schema() ToolSchema {
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
				"content": map[string]any{
					"type":        "string",
					"description": "Full content to write",
				},
			},
			"required": []string{"file_path", "content"},
		},
	}
}

func (t *WriteFileTool) Title(args map[string]any) string {
	p, _ := args["file_path"].(string)
	if p == "" {
		return ""
	}
	content, _ := args["content"].(string)
	lines := strings.Count(content, "\n") + 1
	return fmt.Sprintf("Write %s (%d lines)", filepath.Base(p), lines)
}

func (t *WriteFileTool) Execute(_ context.Context, args map[string]any) ToolResult {
	path, ok := argString(args, "file_path")
	if !ok {
		return errResult("Write: missing required string argument \"file_path\"")
	}
	content, ok := args["content"].(string)
	if !ok {
		return errResult("Write: missing required string argument \"content\"")
	}

	full := ResolveWorkPath(t.WorkDir, path)
	if t.FileStateCache != nil {
		// New files are exempt; existing files must have been read first.
		if _, err := os.Stat(full); err == nil {
			if reason := t.FileStateCache.Verify(full); reason != "" {
				return errResult("Write: " + reason)
			}
		}
	}
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errResult(fmt.Sprintf("Write: cannot create directory %s: %v", dir, err))
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(full)+".tmp-*")
	if err != nil {
		return errResult(fmt.Sprintf("Write: cannot create temp file in %s: %v", dir, err))
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return errResult(fmt.Sprintf("Write: cannot write %s: %v", path, err))
	}
	if err := tmp.Close(); err != nil {
		return errResult(fmt.Sprintf("Write: cannot write %s: %v", path, err))
	}
	if err := os.Rename(tmpName, full); err != nil {
		return errResult(fmt.Sprintf("Write: cannot replace %s: %v", path, err))
	}
	if t.FileStateCache != nil {
		t.FileStateCache.Record(full)
	}
	return ToolResult{Output: fmt.Sprintf("wrote %d bytes to %s", len(content), path)}
}
