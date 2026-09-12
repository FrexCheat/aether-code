package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileTool writes content to a file, creating parent directories as
// needed and replacing existing files. Writes are atomic: content lands in a
// sibling temp file first, then a single rename commits it, so a crash or
// full disk never leaves a half-written file behind. Overwriting an existing
// file requires that the model has read it (and it hasn't changed since).
type WriteFileTool struct {
	WorkDir string
	// FileStateCache gates overwrites on a prior read; nil disables the gate.
	FileStateCache *FileStateCache
}

func (t *WriteFileTool) Name() string { return "write_file" }
func (t *WriteFileTool) Description() string {
	return WriteFileDescription
}
func (t *WriteFileTool) Category() Category { return CategoryWrite }

func (t *WriteFileTool) Schema() map[string]any {
	return toolSchema(t.Name(), t.Description(), map[string]any{
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
	})
}

func (t *WriteFileTool) Execute(_ context.Context, args map[string]any) ToolResult {
	path, ok := argString(args, "file_path")
	if !ok {
		return errResult("write_file: missing required string argument \"file_path\"")
	}
	content, ok := args["content"].(string)
	if !ok {
		return errResult("write_file: missing required string argument \"content\"")
	}

	full := ResolveWorkPath(t.WorkDir, path)
	if t.FileStateCache != nil {
		// New files are exempt; existing files must have been read first.
		if _, err := os.Stat(full); err == nil {
			if ok, msg := t.FileStateCache.Check(full); !ok {
				return errResult("write_file: " + msg)
			}
		}
	}
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errResult(fmt.Sprintf("write_file: cannot create directory %s: %v", dir, err))
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(full)+".tmp-*")
	if err != nil {
		return errResult(fmt.Sprintf("write_file: cannot create temp file in %s: %v", dir, err))
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return errResult(fmt.Sprintf("write_file: cannot write %s: %v", path, err))
	}
	if err := tmp.Close(); err != nil {
		return errResult(fmt.Sprintf("write_file: cannot write %s: %v", path, err))
	}
	if err := os.Rename(tmpName, full); err != nil {
		return errResult(fmt.Sprintf("write_file: cannot replace %s: %v", path, err))
	}
	if t.FileStateCache != nil {
		t.FileStateCache.Update(full)
	}
	return ToolResult{Output: fmt.Sprintf("wrote %d bytes to %s", len(content), path)}
}
