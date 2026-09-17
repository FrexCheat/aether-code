package permissions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRule(t *testing.T) {
	rule, err := ParseRule("Bash(git *)", Allow)
	if err != nil {
		t.Fatalf("ParseRule: %v", err)
	}
	if rule.Tool != "Bash" || rule.Pattern != "git *" || rule.Effect != Allow {
		t.Fatalf("ParseRule = %+v, want Bash / git * / allow", rule)
	}
	if got := rule.String(); got != "Bash(git *)" {
		t.Fatalf("Rule.String() = %q, want %q", got, "Bash(git *)")
	}

	bad := []struct {
		name string
		raw  string
		hint string
	}{
		{"old snake_case name", "read_file(x)", "unknown tool"},
		{"lowercase name", "bash(x)", "unknown tool"},
		{"unknown tool", "Foo(x)", "unknown tool"},
		{"empty pattern", "Bash()", "empty"},
		{"no parentheses", "Bash", "expected Tool(pattern)"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseRule(tt.raw, Allow)
			if err == nil {
				t.Fatalf("ParseRule(%q) succeeded, want error", tt.raw)
			}
			if !strings.Contains(err.Error(), tt.hint) {
				t.Fatalf("ParseRule(%q) error = %q, want it to mention %q", tt.raw, err, tt.hint)
			}
		})
	}
}

func TestRuleMatches(t *testing.T) {
	tests := []struct {
		name     string
		rule     Rule
		toolName string
		content  string
		want     bool
	}{
		{"wildcard prefix", Rule{Tool: "Bash", Pattern: "git *"}, "Bash", "git status", true},
		{"wildcard prefix miss", Rule{Tool: "Bash", Pattern: "git *"}, "Bash", "rm -rf build", false},
		{"wildcard spans slashes", Rule{Tool: "Edit", Pattern: "src/**"}, "Edit", "src/a/b.go", true},
		{"single char wildcard", Rule{Tool: "Read", Pattern: "src/?.go"}, "Read", "src/a.go", true},
		{"single char wildcard miss", Rule{Tool: "Read", Pattern: "src/?.go"}, "Read", "src/ab.go", false},
		{"dot is literal", Rule{Tool: "Read", Pattern: "a.b"}, "Read", "axb", false},
		{"dot matches itself", Rule{Tool: "Read", Pattern: "a.b"}, "Read", "a.b", true},
		{"exact match", Rule{Tool: "Read", Pattern: "README.md"}, "Read", "README.md", true},
		{"exact match is anchored", Rule{Tool: "Read", Pattern: "README.md"}, "Read", "README.md.bak", false},
		{"other tool", Rule{Tool: "Bash", Pattern: "*"}, "Read", "anything", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rule.Matches(tt.toolName, tt.content); got != tt.want {
				t.Fatalf("%s.Matches(%q, %q) = %v, want %v", tt.rule.String(), tt.toolName, tt.content, got, tt.want)
			}
		})
	}
}

func TestExtractContent(t *testing.T) {
	tests := []struct {
		toolName string
		args     map[string]any
		want     string
	}{
		{"Bash", map[string]any{"command": "git status"}, "git status"},
		{"Read", map[string]any{"file_path": "a.go", "limit": 10}, "a.go"},
		{"Write", map[string]any{"file_path": "a.go", "content": "x"}, "a.go"},
		{"Edit", map[string]any{"file_path": "a.go"}, "a.go"},
		{"Glob", map[string]any{"pattern": "**/*.go", "path": "src"}, "**/*.go"},
		{"Grep", map[string]any{"pattern": "func main"}, "func main"},
		{"unknown tool", map[string]any{"command": "x"}, ""},
		{"missing argument", map[string]any{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.toolName, func(t *testing.T) {
			if got := ExtractContent(tt.toolName, tt.args); got != tt.want {
				t.Fatalf("ExtractContent(%q) = %q, want %q", tt.toolName, got, tt.want)
			}
		})
	}
}

func TestEvaluateRules(t *testing.T) {
	deny := Rule{Tool: "Bash", Pattern: "rm *", Effect: Deny}
	ask := Rule{Tool: "Bash", Pattern: "git *", Effect: Ask}
	allow := Rule{Tool: "Bash", Pattern: "git status", Effect: Allow}

	tests := []struct {
		name    string
		rules   []Rule
		content string
		want    Effect
	}{
		{"deny beats allow", []Rule{allow, deny}, "rm -rf build", Deny},
		{"deny beats allow reversed", []Rule{deny, allow}, "rm -rf build", Deny},
		{"ask beats allow", []Rule{allow, ask}, "git status", Ask},
		{"ask beats allow reversed", []Rule{ask, allow}, "git status", Ask},
		{"allow alone", []Rule{allow}, "git status", Allow},
		{"no match", []Rule{deny, ask}, "ls", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateRules(tt.rules, "Bash", tt.content)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("EvaluateRules = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("EvaluateRules = nil, want %v", tt.want)
			}
			if got.Effect != tt.want {
				t.Fatalf("EvaluateRules = %v, want %v", got.Effect, tt.want)
			}
			if got.Reason == "" {
				t.Fatal("EvaluateRules returned an empty reason")
			}
		})
	}
}

// newTestEngine isolates both rule layers: the user file lives under a fake
// home, the project files under a fresh working directory.
func newTestEngine(t *testing.T) (*RuleEngine, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	return NewRuleEngine(work), home, work
}

func writeRulesFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ruleSpellings(rules []Rule) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.String())
	}
	return out
}

func TestRuleEngineMerge(t *testing.T) {
	engine, home, work := newTestEngine(t)
	writeRulesFile(t, filepath.Join(home, ".aethercode", "permissions.yaml"),
		"rules:\n  - rule: Bash(git *)\n    effect: allow\n")
	writeRulesFile(t, filepath.Join(work, ".aethercode", "permissions.yaml"),
		"rules:\n  - rule: Read(src/**)\n    effect: allow\n")
	writeRulesFile(t, filepath.Join(work, ".aethercode", "permissions.local.yaml"),
		"rules:\n  - rule: Bash(rm *)\n    effect: deny\n")

	got := ruleSpellings(engine.Snapshot())
	want := []string{"Bash(git *)", "Read(src/**)", "Bash(rm *)"}
	if !equalStrings(got, want) {
		t.Fatalf("Snapshot = %q, want %q", got, want)
	}
}

func TestRuleEngineReload(t *testing.T) {
	engine, home, work := newTestEngine(t)
	path := filepath.Join(work, ".aethercode", "permissions.yaml")
	writeRulesFile(t, path, "rules:\n  - rule: Bash(git *)\n    effect: allow\n")
	if got := ruleSpellings(engine.Snapshot()); !equalStrings(got, []string{"Bash(git *)"}) {
		t.Fatalf("Snapshot = %q, want [Bash(git *)]", got)
	}
	_ = home

	// A rewrite is picked up on the next snapshot, with no restart.
	writeRulesFile(t, path, "rules:\n  - rule: Bash(git status)\n    effect: allow\n  - rule: Bash(npm *)\n    effect: ask\n")
	got := ruleSpellings(engine.Snapshot())
	want := []string{"Bash(git status)", "Bash(npm *)"}
	if !equalStrings(got, want) {
		t.Fatalf("after rewrite Snapshot = %q, want %q", got, want)
	}

	// A deleted file stops contributing.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := engine.Snapshot(); len(got) != 0 {
		t.Fatalf("after delete Snapshot = %q, want empty", ruleSpellings(got))
	}
}

func TestRuleEngineTolerance(t *testing.T) {
	engine, _, work := newTestEngine(t)
	path := filepath.Join(work, ".aethercode", "permissions.yaml")
	// One malformed entry, one unknown effect, one unknown tool: each is
	// skipped on its own, and the good entry still counts.
	writeRulesFile(t, path, `rules:
  - rule: "not a rule"
    effect: allow
  - rule: Bash(ls *)
    effect: sometimes
  - rule: Foo(x)
    effect: allow
  - rule: Bash(git *)
    effect: allow
`)
	got := ruleSpellings(engine.Snapshot())
	if !equalStrings(got, []string{"Bash(git *)"}) {
		t.Fatalf("Snapshot = %q, want only [Bash(git *)]", got)
	}

	// A broken file degrades to empty rules instead of failing.
	writeRulesFile(t, path, "rules: [ this is: not: yaml\n")
	if got := engine.Snapshot(); len(got) != 0 {
		t.Fatalf("broken file Snapshot = %q, want empty", ruleSpellings(got))
	}
}

func TestRuleEngineAppend(t *testing.T) {
	engine, _, work := newTestEngine(t)
	rule := Rule{Tool: "Bash", Pattern: "git status", Effect: Allow}
	if err := engine.AppendLocalRule(rule); err != nil {
		t.Fatalf("AppendLocalRule: %v", err)
	}
	if got := ruleSpellings(engine.Snapshot()); !equalStrings(got, []string{"Bash(git status)"}) {
		t.Fatalf("after append Snapshot = %q, want [Bash(git status)]", got)
	}

	// A second append keeps the first entry.
	if err := engine.AppendLocalRule(Rule{Tool: "Read", Pattern: "README.md", Effect: Allow}); err != nil {
		t.Fatalf("second AppendLocalRule: %v", err)
	}
	got := ruleSpellings(engine.Snapshot())
	want := []string{"Bash(git status)", "Read(README.md)"}
	if !equalStrings(got, want) {
		t.Fatalf("after second append Snapshot = %q, want %q", got, want)
	}

	body, err := os.ReadFile(filepath.Join(work, ".aethercode", "permissions.local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Bash(git status)", "Read(README.md)"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("local file does not contain %q:\n%s", want, body)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
