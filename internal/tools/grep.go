package tools

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// GrepMatchLimit caps reported matches so a hot pattern can't flood the
// result; the marker tells the model to narrow the pattern instead.
const GrepMatchLimit = 200

// GrepTool searches file contents line by line with a regular expression,
// optionally restricted to a directory and/or an include glob on base names.
type GrepTool struct {
	WorkDir string
}

func (t *GrepTool) Name() string { return "grep" }
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

// Title summarizes the call for the UI: "Grep: TODO".
func (t *GrepTool) Title(args map[string]any) string {
	if pattern, _ := args["pattern"].(string); pattern != "" {
		return "Grep: " + pattern
	}
	return ""
}

func (t *GrepTool) Execute(_ context.Context, args map[string]any) ToolResult {
	pattern, ok := argString(args, "pattern")
	if !ok {
		return errResult("grep: missing required string argument \"pattern\"")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return errResult(fmt.Sprintf("grep: invalid regular expression %q: %v", pattern, err))
	}

	root := t.WorkDir
	if p, ok := args["path"].(string); ok && p != "" {
		root = ResolveWorkPath(t.WorkDir, p)
	} else if _, present := args["path"]; present {
		return errResult("grep: argument \"path\" must be a string")
	}
	include, hasInclude := "", false
	if p, ok := args["include"].(string); ok && p != "" {
		include, hasInclude = p, true
	} else if _, present := args["include"]; present {
		return errResult("grep: argument \"include\" must be a string")
	}

	var out strings.Builder
	matches := 0
	truncated := false

	searchFile := func(path string) error {
		if matches >= GrepMatchLimit {
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
				if matches >= GrepMatchLimit {
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
		return errResult(fmt.Sprintf("grep: cannot access %s: %v", root, statErr))
	case !info.IsDir():
		if err := searchFile(root); err != nil {
			return errResult(fmt.Sprintf("grep: %v", err))
		}
	default:
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable entries are skipped
			}
			if d.IsDir() {
				if SkipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if hasInclude {
				if ok, err := filepath.Match(include, d.Name()); err != nil || !ok {
					return nil
				}
			}
			return searchFile(path)
		})
		if walkErr != nil {
			return errResult(fmt.Sprintf("grep: walk failed: %v", walkErr))
		}
	}

	if matches == 0 {
		return ToolResult{Output: fmt.Sprintf("no matches for %q under %s", pattern, root)}
	}
	result := strings.TrimSuffix(out.String(), "\n")
	if truncated {
		result += fmt.Sprintf("\n... (stopped at %d matches; narrow the pattern or path to see more)", GrepMatchLimit)
	}
	return ToolResult{Output: TruncateOutput(result, MaxOutputChars)}
}
