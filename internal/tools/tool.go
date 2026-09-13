// Package tools defines the unified tool abstraction consumed by the agent
// layer: the Tool interface every implementation satisfies, the structured
// ToolResult fed back to the model, and the Registry that owns lookup and
// schema generation for both wire protocols.
//
// Layout (flat, one file per tool):
//
//	tool.go       — Tool, ToolResult, Category, Registry, shared helpers
//	read_file.go  — read file contents (offset/limit paging)
//	write_file.go — atomic file write (temp file + rename)
//	edit_file.go  — unique-match replacement with diff output
//	bash.go       — shell command execution with timeout
//	glob.go       — pattern-based file search
//	grep.go       — regex content search
//
// Support files:
//
//	descriptions.go      — model-facing prompt text, one const per tool
//	file_state_cache.go  — read-before-edit gate shared by read/write/edit
package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxOutputChars is the truncation threshold for one tool result before it
// enters the conversation history. Oversized results are cut with a marker
// rather than spilled to disk.
const MaxOutputChars = 50000

// Category classifies a tool by its side-effect level.
type Category string

const (
	CategoryRead    Category = "read"
	CategoryWrite   Category = "write"
	CategoryCommand Category = "command"
)

// ToolResult is what a tool execution returns to the model. Failures are
// values, not panics: IsError marks them so the model can adjust and retry.
type ToolResult struct {
	Output  string
	IsError bool
}

// Tool is the contract every tool implements. Schema returns the
// provider-neutral description {"name", "description", "input_schema"};
// adapters translate it per protocol.
type Tool interface {
	Name() string
	Description() string
	Category() Category
	Schema() map[string]any
	Execute(ctx context.Context, args map[string]any) ToolResult
}

// Registry is the central tool directory. Registration happens once at
// startup; lookup and schema generation are read-only afterwards.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

// Register adds a tool. Duplicate names are a programming error at startup
// and panic rather than silently shadowing.
func (r *Registry) Register(t Tool) {
	name := t.Name()
	if _, dup := r.tools[name]; dup {
		panic("tools: duplicate registration of " + name)
	}
	r.tools[name] = t
}

// Get returns the tool registered under name, or nil.
func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

// GetAllSchemas builds the tool list for one request in the single neutral
// shape {"name", "description", "input_schema"}; each llm adapter translates
// it into its own API format.
//
// Tools are walked in sorted-name order, not map order. Go map iteration is
// randomized; unsorted, the same tool set would serialize into a different
// array every request, and the prompt-cache prefix after the tools block
// would be invalidated even though nothing changed semantically.
func (r *Registry) GetAllSchemas() []map[string]any {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	schemas := make([]map[string]any, 0, len(names))
	for _, name := range names {
		schemas = append(schemas, r.tools[name].Schema())
	}
	return schemas
}

// toolSchema assembles the neutral schema shape shared by every tool's
// Schema() implementation.
func toolSchema(name, description string, inputSchema map[string]any) map[string]any {
	return map[string]any{
		"name":         name,
		"description":  description,
		"input_schema": inputSchema,
	}
}

// TruncateOutput caps a tool result at max characters. The marker tells the
// model the output was cut, so it knows the result is partial.
func TruncateOutput(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n\n[output truncated: showing first %d of %d characters]", max, len(s))
}

// ResolveWorkPath resolves a tool path argument against the working directory.
// Relative paths are always interpreted relative to workDir (N3); absolute
// paths pass through unchanged.
func ResolveWorkPath(workDir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(workDir, path))
}

// SkipDirs are directory names never descended into by glob/grep. They hold
// dependency caches and VCS data whose contents are noise for code search.
var SkipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, ".venv": true, "venv": true,
	"__pycache__": true, ".mypy_cache": true, ".tox": true,
	"dist": true, "build": true, "target": true,
}

// looksBinary reports whether a chunk of file content is likely binary
// (contains a NUL byte in the first 8 KiB), used by grep to skip non-text.
func looksBinary(chunk []byte) bool {
	if len(chunk) > 8192 {
		chunk = chunk[:8192]
	}
	return strings.IndexByte(string(chunk), 0) >= 0
}

// isTextFile opens path and reports whether the leading chunk looks like text.
func isTextFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	chunk := make([]byte, 8192)
	n, _ := f.Read(chunk)
	return !looksBinary(chunk[:n])
}
