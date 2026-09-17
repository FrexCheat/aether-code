package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const MaxOutputChars = 50000

type Category string

const (
	CategoryRead    Category = "read"
	CategoryWrite   Category = "write"
	CategoryCommand Category = "command"
)

type ToolResult struct {
	Output  string
	IsError bool
	Diff    bool
}

type Tool interface {
	Name() string
	Description() string
	Category() Category
	Schema() ToolSchema
	Execute(ctx context.Context, args map[string]any) ToolResult
}

type ToolSchema struct {
	Name        string
	Description string
	InputSchema map[string]any
}

type TitledTool interface {
	Title(args map[string]any) string
}

type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(t Tool) {
	name := t.Name()
	if _, dup := r.tools[name]; dup {
		panic("tools: duplicate registration of " + name)
	}
	r.tools[name] = t
}

func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

// Schemas returns the schema of every registered tool, ordered by name.
func (r *Registry) Schemas() []ToolSchema {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	schemas := make([]ToolSchema, 0, len(names))
	for _, name := range names {
		schemas = append(schemas, r.tools[name].Schema())
	}
	return schemas
}

func truncateTitle(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

func TruncateOutput(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n\n[output truncated: showing first %d of %d characters]", max, len(s))
}

func ResolveWorkPath(workDir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(workDir, path))
}

var SkipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, ".venv": true, "venv": true,
	"__pycache__": true, ".mypy_cache": true, ".tox": true,
	"dist": true, "build": true, "target": true,
}

func looksBinary(chunk []byte) bool {
	if len(chunk) > 8192 {
		chunk = chunk[:8192]
	}
	return strings.IndexByte(string(chunk), 0) >= 0
}

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
