package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GlobTool matches file paths against a glob pattern, never descending into
// dependency/VCS directories. `**` spans directory levels; the pattern is
// matched against paths relative to the search root.
type GlobTool struct {
	WorkDir string
}

func (t *GlobTool) Name() string { return "glob" }
func (t *GlobTool) Description() string {
	return GlobDescription
}
func (t *GlobTool) Category() Category { return CategoryRead }

func (t *GlobTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: t.Description(),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Glob pattern relative to the search root; ** matches any number of directories",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Directory to search in (default: working directory)",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

// Title summarizes the call for the UI: "Glob: **/*.go".
func (t *GlobTool) Title(args map[string]any) string {
	if pattern, _ := args["pattern"].(string); pattern != "" {
		return "Glob: " + pattern
	}
	return ""
}

func (t *GlobTool) Execute(_ context.Context, args map[string]any) ToolResult {
	pattern, ok := argString(args, "pattern")
	if !ok {
		return errResult("glob: missing required string argument \"pattern\"")
	}
	root := t.WorkDir
	if p, ok := args["path"].(string); ok && p != "" {
		root = ResolveWorkPath(t.WorkDir, p)
	} else if _, present := args["path"]; present {
		return errResult("glob: argument \"path\" must be a string")
	}

	// filepath.Match has no **; expand the first ** into a zero-or-more-levels
	// match by walking every directory and matching the remainder.
	var matches []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // vanished between listing and walk: skip, not fail
			}
			return err
		}
		if d.IsDir() {
			if SkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if matchGlob(pattern, rel) {
			matches = append(matches, rel)
		}
		return nil
	})
	if err != nil {
		return errResult(fmt.Sprintf("glob: walk failed: %v", err))
	}
	sort.Strings(matches)

	if len(matches) == 0 {
		return ToolResult{Output: fmt.Sprintf("no files match %q under %s", pattern, root)}
	}
	return ToolResult{Output: TruncateOutput(strings.Join(matches, "\n"), MaxOutputChars)}
}

// matchGlob matches rel against pattern where ** spans directory levels.
// Each ** segment consumes zero or more leading path segments, so
// **/*.go matches a.go and also a/b/c.go.
func matchGlob(pattern, rel string) bool {
	if !strings.Contains(pattern, "**") {
		ok, err := filepath.Match(pattern, rel)
		return err == nil && ok
	}
	segments := strings.Split(filepath.ToSlash(pattern), "/")
	return matchGlobSegments(segments, strings.Split(filepath.ToSlash(rel), "/"))
}

func matchGlobSegments(pattern, parts []string) bool {
	for {
		switch {
		case len(pattern) == 0:
			return len(parts) == 0
		case pattern[0] == "**":
			// ** matches zero or more segments; try consuming one at a time.
			for i := 0; i <= len(parts); i++ {
				if matchGlobSegments(pattern[1:], parts[i:]) {
					return true
				}
			}
			return false
		case len(parts) == 0:
			return false
		}
		ok, err := filepath.Match(pattern[0], parts[0])
		if err != nil || !ok {
			return false
		}
		pattern, parts = pattern[1:], parts[1:]
	}
}
