package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const grepMatchLimit = 200

type GrepTool struct {
	WorkDir string
}

func (t *GrepTool) Name() string { return "Grep" }

func (t *GrepTool) Spec() Spec {
	// Rules match the pattern argument; the boundary checks the optional path.
	return Spec{RuleArg: "pattern", BoundaryArg: "path",
		PromptLine: "搜文件内容用 Grep，而不是 grep、rg 或 ag 命令"}
}
func (t *GrepTool) Description() string {
	return GrepDescription
}
func (t *GrepTool) Category() Category { return CategoryRead }

func (t *GrepTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: t.Description(),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Go regular expression to search for",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Directory (or single file) to search in (default: working directory)",
				},
				"include": map[string]any{
					"type":        "string",
					"description": "Glob filtering which file names are searched, e.g. \"*.go\"",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

func (t *GrepTool) Title(args map[string]any) string {
	if pattern, _ := args["pattern"].(string); pattern != "" {
		return "Grep(" + pattern + ")"
	}
	return ""
}

func (t *GrepTool) Execute(_ context.Context, args map[string]any) ToolResult {
	pattern, ok := argString(args, "pattern")
	if !ok {
		return errResult("Grep: missing required string argument \"pattern\"")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return errResult(fmt.Sprintf("Grep: invalid regular expression %q: %v", pattern, err))
	}

	path, err := optionalStringArg(args, "path")
	if err != nil {
		return errResult("Grep: " + err.Error())
	}
	root := t.WorkDir
	if path != "" {
		root = ResolveWorkPath(t.WorkDir, path)
	}
	include, err := optionalStringArg(args, "include")
	if err != nil {
		return errResult("Grep: " + err.Error())
	}

	var out strings.Builder
	matches := 0
	truncated := false

	searchFile := func(path string) error {
		if matches >= grepMatchLimit {
			truncated = true
			return nil
		}
		if !isTextFile(path) {
			return nil // binary files carry no line-oriented matches
		}
		f, err := os.Open(path)
		if err != nil {
			return nil // unreadable files are skipped, not fatal
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if re.MatchString(line) {
				fmt.Fprintf(&out, "%s:%d: %s\n", path, lineNo, line)
				matches++
				if matches >= grepMatchLimit {
					truncated = true
					break
				}
			}
		}
		if scanner.Err() != nil {
			return nil // decode/read errors: skip the file
		}
		return nil
	}

	info, statErr := os.Stat(root)
	switch {
	case statErr != nil:
		return errResult(fmt.Sprintf("Grep: cannot access %s: %v", root, statErr))
	case !info.IsDir():
		if err := searchFile(root); err != nil {
			return errResult(fmt.Sprintf("Grep: %v", err))
		}
	default:
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable entries are skipped
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if include != "" {
				if ok, err := filepath.Match(include, d.Name()); err != nil || !ok {
					return nil
				}
			}
			return searchFile(path)
		})
		if walkErr != nil {
			return errResult(fmt.Sprintf("Grep: walk failed: %v", walkErr))
		}
	}

	if matches == 0 {
		return ToolResult{Output: fmt.Sprintf("no matches for %q under %s", pattern, root)}
	}
	result := strings.TrimSuffix(out.String(), "\n")
	if truncated {
		result += fmt.Sprintf("\n... (stopped at %d matches; narrow the pattern or path to see more)", grepMatchLimit)
	}
	return ToolResult{Output: result}
}

// isTextFile reports whether a file looks like text: a NUL byte in its first
// chunk is the usual tell that it does not.
func isTextFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	chunk := make([]byte, 8192)
	n, _ := f.Read(chunk)
	return bytes.IndexByte(chunk[:n], 0) < 0
}
