package permissions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FrexCheat/aether-code/internal/tools"
)

func TestRuleWarningsOnBrokenFile(t *testing.T) {
	work := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	local := filepath.Join(work, ".aether", "permissions.local.yaml")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("rules: [unclosed"), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := NewChecker(work)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	warnings := c.RuleWarnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "invalid YAML") {
		t.Fatalf("warnings = %v, want one invalid-YAML notice", warnings)
	}
	// The ladder itself stays warning-free — a broken file contributes no
	// rules rather than breaking checks.
	if got := c.Check(&tools.BashTool{WorkDir: work}, map[string]any{"command": "ls"}); got.Effect != Ask {
		t.Errorf("check with a broken rule file = %s, want ask", got.Effect)
	}
	// A good file produces no warning.
	if err := os.WriteFile(local, []byte("rules:\n  - rule: \"Bash(ls *)\"\n    effect: allow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if warnings := c.RuleWarnings(); len(warnings) != 0 {
		t.Errorf("warnings after fixing the file = %v, want none", warnings)
	}
}

func TestPatternMatches(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		value   string
		want    bool
	}{
		{"exact", "ls -la", "ls -la", true},
		{"exact is anchored", "ls -la", "ls -la extra", false},
		{"head is anchored", "s *", "ls -la", false},
		{"star matches the rest", "ls *", "ls -la", true},
		{"star matches empty", "ls*", "ls", true},
		{"trailing star", "*la", "ls -la", true},
		{"escaped star is literal", `a\*b`, "a*b", true},
		{"escaped star is not a wildcard", `a\*b`, "aXb", false},
		{"escaped backslash", `a\\b`, `a\b`, true},
		{"unicode", "读取 *", "读取 文件", true},
		{"mcp wildcard", "mcp__srv__*", "mcp__srv__tool", true},
		{"mcp server boundary", "mcp__srv__*", "mcp__other__tool", false},
		{"json prefix addresses mcp args", `{"path":"/etc/*`, `{"path":"/etc/passwd"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := patternMatches(tt.pattern, tt.value); got != tt.want {
				t.Errorf("patternMatches(%q, %q) = %v, want %v", tt.pattern, tt.value, got, tt.want)
			}
		})
	}
}

func TestParseRule(t *testing.T) {
	valid := []string{"Bash(ls *)", "Read(/tmp/**)", "mcp__srv__tool(*)", "mcp__srv__*(*)"}
	for _, raw := range valid {
		if _, err := ParseRule(raw, Allow); err != nil {
			t.Errorf("ParseRule(%q) = %v, want nil", raw, err)
		}
	}
	invalid := []string{"Bash", "Bash()", "Unknown(x)", "(x)", "Bash(ls *"}
	for _, raw := range invalid {
		if _, err := ParseRule(raw, Allow); err == nil {
			t.Errorf("ParseRule(%q) = nil, want an error", raw)
		}
	}
}

func TestEvaluateRulesPrecedence(t *testing.T) {
	rules := []Rule{
		{Tool: "Bash", Pattern: "ls *", Effect: Allow},
		{Tool: "Bash", Pattern: "ls -la", Effect: Ask},
	}
	if d := EvaluateRules(rules, "Bash", "ls -la"); d == nil || d.Effect != Ask {
		t.Errorf("ask outranks allow for one call: %+v", d)
	}
	rules = append(rules, Rule{Tool: "Bash", Pattern: "*", Effect: Deny})
	if d := EvaluateRules(rules, "Bash", "ls -la"); d == nil || d.Effect != Deny {
		t.Errorf("deny outranks everything: %+v", d)
	}
}
