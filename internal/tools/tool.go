package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
)

// maxOutputChars caps how much of a result reaches the model.
const maxOutputChars = 50000

type Category string

const (
	CategoryRead    Category = "read"
	CategoryWrite   Category = "write"
	CategoryCommand Category = "command"
	// CategoryMCP marks tools coming from an MCP server, including the
	// McpCall dispatcher. Outside bypass mode the permission ladder asks
	// for these calls; their execution stays serial.
	CategoryMCP Category = "mcp"
)

type ToolResult struct {
	Output  string
	IsError bool
	IsDiff  bool // Output is a rendered diff, to be colored line by line
	// ToolRefs names the deferred MCP tools a result surfaced; adapters may
	// render them as tool_reference blocks instead of plain text.
	ToolRefs []string
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
	// Deferred marks a tool sent with the provider's deferred-loading flag:
	// on the wire every round, its schema hidden until ToolSearch surfaces it.
	Deferred bool
}

type TitledTool interface {
	Title(args map[string]any) string
}

// Dispatcher is implemented by tools that pick another registered tool from
// their arguments and hand the call over (the McpCall dispatcher). The agent
// resolves the target before the permission check, so rules, dialogs and
// grants anchor on the target tool's name, whichever way the call arrived.
type Dispatcher interface {
	// Target returns the tool this call actually runs, with the arguments
	// adapted to it, or an error naming what went wrong.
	Target(args map[string]any) (Tool, map[string]any, error)
}

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := t.Name()
	if _, dup := r.tools[name]; dup {
		panic("tools: duplicate registration of " + name)
	}
	r.tools[name] = t
}

func (r *Registry) Get(name string) Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

// Schemas returns every tool's schema, ordered by name so the request stays
// byte-stable across rounds.
func (r *Registry) Schemas() []ToolSchema {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	slices.Sort(names)

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

// TruncateOutput caps a result so one call cannot blow up the context. MCP
// results share the bound with the built-in tools.
func TruncateOutput(s string) string {
	if len(s) <= maxOutputChars {
		return s
	}
	return s[:maxOutputChars] +
		fmt.Sprintf("\n\n[output truncated: showing first %d of %d characters]", maxOutputChars, len(s))
}

func ResolveWorkPath(workDir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(workDir, path))
}

// skipDirs are directories no search tool descends into.
var skipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, ".venv": true, "venv": true,
	"__pycache__": true, ".mypy_cache": true, ".tox": true,
	"dist": true, "build": true, "target": true,
}
