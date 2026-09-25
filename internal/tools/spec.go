package tools

// MCPToolPrefix opens every delegated MCP tool name (mcp__<server>__<tool>).
// This is the one literal: the MCP package builds names from it and the
// permission layer accepts rules qualified by it.
const MCPToolPrefix = "mcp__"

// Spec is the metadata a tool contributes beyond its behavior: how a
// permission rule addresses its calls, which argument the workspace boundary
// resolves, and the bullet the system prompt carries for it.
type Spec struct {
	// RuleArg is the argument a rule pattern is matched against; "" means the
	// tool's rules address its name only.
	RuleArg string
	// RuleArgIsPath marks RuleArg as a filesystem path, so rule matching also
	// tries the resolved form that recorded grants use.
	RuleArgIsPath bool
	// BoundaryArg is the argument resolved against the workspace boundary;
	// "" means the boundary has nothing to check for this tool.
	BoundaryArg string
	// PromptLine is the bullet the system prompt lists under 使用你的工具.
	// It lives beside the tool so a name and its guidance share one source.
	PromptLine string
}

// Specified is implemented by tools whose calls permissions can address
// beyond the tool name. MCP wrapper tools deliberately do not implement it:
// their rules anchor on the qualified name and the JSON-encoded arguments.
type Specified interface{ Spec() Spec }

// BuiltinSpec pairs a built-in's name with its spec.
type BuiltinSpec struct {
	Name string
	Spec Spec
}

// BuiltinSpecs is the one enumeration of the built-in vocabulary, in prompt
// order; the names and specs are read off zero-value instances, so this list
// and the registry cannot drift apart.
func BuiltinSpecs() []BuiltinSpec {
	builtins := []Tool{
		&ReadFileTool{}, &EditFileTool{}, &WriteFileTool{},
		&GlobTool{}, &GrepTool{}, &BashTool{},
	}
	specs := make([]BuiltinSpec, 0, len(builtins))
	for _, t := range builtins {
		specs = append(specs, BuiltinSpec{Name: t.Name(), Spec: t.(Specified).Spec()})
	}
	return specs
}

// BuiltinNames lists the built-in tool names in manifest order.
func BuiltinNames() []string {
	specs := BuiltinSpecs()
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	return names
}

// NewBuiltinRegistry registers the six built-ins around workDir; the tools
// that read and write files share one file-state cache.
func NewBuiltinRegistry(workDir string, fileStates *FileStateCache) *Registry {
	registry := NewRegistry()
	registry.Register(&ReadFileTool{WorkDir: workDir, FileStateCache: fileStates})
	registry.Register(&WriteFileTool{WorkDir: workDir, FileStateCache: fileStates})
	registry.Register(&EditFileTool{WorkDir: workDir, FileStateCache: fileStates})
	registry.Register(&BashTool{WorkDir: workDir})
	registry.Register(&GlobTool{WorkDir: workDir})
	registry.Register(&GrepTool{WorkDir: workDir})
	return registry
}
