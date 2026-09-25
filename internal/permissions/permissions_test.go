package permissions

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/FrexCheat/aether-code/internal/tools"
)

// fakeTool stands in for an MCP wrapper: a tool with no Spec, so rules
// address its qualified name and JSON-encoded arguments.
type fakeTool struct {
	name     string
	category tools.Category
}

func (f *fakeTool) Name() string             { return f.name }
func (f *fakeTool) Description() string      { return "fake" }
func (f *fakeTool) Category() tools.Category { return f.category }
func (f *fakeTool) Schema() tools.ToolSchema { return tools.ToolSchema{Name: f.name} }
func (f *fakeTool) Execute(context.Context, map[string]any) tools.ToolResult {
	return tools.ToolResult{}
}

func newTestChecker(t *testing.T, workDir string) *Checker {
	t.Helper()
	c, err := NewChecker(workDir)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	return c
}

func TestCheckLadder(t *testing.T) {
	work := t.TempDir()
	bash := &tools.BashTool{WorkDir: work}
	read := &tools.ReadFileTool{WorkDir: work}
	write := &tools.WriteFileTool{WorkDir: work}
	glob := &tools.GlobTool{WorkDir: work}
	mcp := &fakeTool{name: "mcp__srv__tool", category: tools.CategoryMCP}
	inside := filepath.Join(work, "note.txt")

	tests := []struct {
		name  string
		setup func(*Checker)
		tool  tools.Tool
		args  map[string]any
		want  Effect
	}{
		{"blacklist denies", nil, bash, map[string]any{"command": "rm -rf /"}, Deny},
		{
			"the rule files stay protected", nil, write,
			map[string]any{"file_path": filepath.Join(work, ".aether", "permissions.local.yaml")}, Deny,
		},
		{
			"deny rule beats everything",
			func(c *Checker) { c.AddSessionRule(Rule{Tool: "Bash", Pattern: "git push*", Effect: Deny}) },
			bash, map[string]any{"command": "git push gitea main"}, Deny,
		},
		{
			"allow rule admits the command",
			func(c *Checker) { c.AddSessionRule(Rule{Tool: "Bash", Pattern: "git status*", Effect: Allow}) },
			bash, map[string]any{"command": "git status"}, Allow,
		},
		{
			"an uncovered subcommand falls through",
			func(c *Checker) { c.AddSessionRule(Rule{Tool: "Bash", Pattern: "git status*", Effect: Allow}) },
			bash, map[string]any{"command": "git status && ls"}, Ask,
		},
		{"read inside the workspace", nil, read, map[string]any{"file_path": inside}, Allow},
		{"read outside the workspace asks", nil, read, map[string]any{"file_path": "/etc/hostname"}, Ask},
		{"glob path outside the workspace asks", nil, glob, map[string]any{"pattern": "*.go", "path": "/etc"}, Ask},
		{"glob without a path stays a read", nil, glob, map[string]any{"pattern": "*.go"}, Allow},
		{"write asks in default mode", nil, write, map[string]any{"file_path": inside}, Ask},
		{"mcp asks by default", nil, mcp, map[string]any{"path": "/etc/x"}, Ask},
		{
			"mcp name wildcard rule allows",
			func(c *Checker) { c.AddSessionRule(Rule{Tool: "mcp__srv__*", Pattern: "*", Effect: Allow}) },
			mcp, map[string]any{"path": "/etc/x"}, Allow,
		},
		{
			"mcp argument deny bites",
			func(c *Checker) {
				c.AddSessionRule(Rule{Tool: "mcp__srv__tool", Pattern: `{"path":"/etc/*`, Effect: Deny})
			},
			mcp, map[string]any{"path": "/etc/passwd"}, Deny,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestChecker(t, work)
			if tt.setup != nil {
				tt.setup(c)
			}
			if got := c.Check(tt.tool, tt.args).Effect; got != tt.want {
				t.Errorf("Check() = %s, want %s", got, tt.want)
			}
		})
	}

	// Mode matrix: acceptEdits admits writes, bypass admits everything.
	c := newTestChecker(t, work)
	c.SetMode(ModeAcceptEdits)
	if got := c.Check(write, map[string]any{"file_path": inside}).Effect; got != Allow {
		t.Errorf("acceptEdits write = %s, want allow", got)
	}
	c.SetMode(ModeBypass)
	if got := c.Check(glob, map[string]any{"pattern": "*.go", "path": "/etc"}).Effect; got != Allow {
		t.Errorf("bypass glob outside = %s, want allow", got)
	}
}

func TestMcpRuleValueAndSuggestions(t *testing.T) {
	c := newTestChecker(t, t.TempDir())
	mcp := &fakeTool{name: "mcp__srv__tool", category: tools.CategoryMCP}

	// No arguments: the name-only form rules had before arguments were
	// addressable.
	q := c.Question(mcp, map[string]any{})
	if q.Action != "" {
		t.Errorf("Action = %q, want empty for an argument-less call", q.Action)
	}
	if len(q.Rules) != 1 || q.Rules[0].Pattern != "*" {
		t.Errorf("rules = %+v, want one name-only wildcard", q.Rules)
	}

	// With arguments the rule value is the stable JSON encoding, so a grant
	// can address them.
	q = c.Question(mcp, map[string]any{"path": "/etc/passwd"})
	if q.Action != `{"path":"/etc/passwd"}` {
		t.Errorf("Action = %q, want the JSON encoding", q.Action)
	}
	if len(q.Rules) != 1 || q.Rules[0].Pattern != `{"path":"/etc/passwd"}*` {
		t.Errorf("rules = %+v, want a JSON-prefix grant", q.Rules)
	}
}
