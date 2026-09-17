package permissions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FrexCheat/aether-code/internal/tools"
)

// testTool is a stand-in for a registry tool: the checker only asks a tool for
// its name and category.
type testTool struct {
	name     string
	category tools.Category
}

func (t testTool) Name() string             { return t.name }
func (t testTool) Description() string      { return "" }
func (t testTool) Category() tools.Category { return t.category }
func (t testTool) Schema() tools.ToolSchema { return tools.ToolSchema{Name: t.name} }
func (t testTool) Execute(context.Context, map[string]any) tools.ToolResult {
	return tools.ToolResult{}
}

var (
	bashTool  = testTool{name: "Bash", category: tools.CategoryCommand}
	readTool  = testTool{name: "Read", category: tools.CategoryRead}
	writeTool = testTool{name: "Write", category: tools.CategoryWrite}
	editTool  = testTool{name: "Edit", category: tools.CategoryWrite}
	globTool  = testTool{name: "Glob", category: tools.CategoryRead}
)

// newTestChecker builds a checker whose rule files are all inside temp dirs,
// so the developer's own ~/.aethercode never leaks into a test.
func newTestChecker(t *testing.T, mode Mode) (*Checker, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	work := t.TempDir()
	checker, err := NewChecker(work)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	checker.SetMode(mode)
	return checker, work
}

func writeLocalRules(t *testing.T, work, body string) {
	t.Helper()
	writeRulesFile(t, filepath.Join(work, ".aethercode", "permissions.local.yaml"), body)
}

func TestMatrix(t *testing.T) {
	tests := []struct {
		mode     Mode
		category tools.Category
		want     Effect
	}{
		{ModeDefault, tools.CategoryRead, Allow},
		{ModeDefault, tools.CategoryWrite, Ask},
		{ModeDefault, tools.CategoryCommand, Ask},

		{ModeAcceptEdits, tools.CategoryRead, Allow},
		{ModeAcceptEdits, tools.CategoryWrite, Allow},
		{ModeAcceptEdits, tools.CategoryCommand, Ask},

		{ModeBypass, tools.CategoryRead, Allow},
		{ModeBypass, tools.CategoryWrite, Allow},
		{ModeBypass, tools.CategoryCommand, Allow},
	}
	for _, tt := range tests {
		t.Run(string(tt.mode)+"/"+string(tt.category), func(t *testing.T) {
			if got := matrixEffect(tt.mode, tt.category); got != tt.want {
				t.Fatalf("matrixEffect(%s, %s) = %s, want %s", tt.mode, tt.category, got, tt.want)
			}
		})
	}
}

func TestModeCycle(t *testing.T) {
	// A session starts in the default mode and cycles through the three.
	checker, _ := newTestChecker(t, ModeDefault)
	if got := checker.Mode(); got != ModeDefault {
		t.Fatalf("a new checker starts in %s, want default", got)
	}
	for _, want := range []Mode{ModeAcceptEdits, ModeBypass, ModeDefault} {
		checker.SetMode(NextMode(checker.Mode()))
		if got := checker.Mode(); got != want {
			t.Fatalf("after a cycle step the mode is %s, want %s", got, want)
		}
	}
}

// TestCheckBlacklist covers the layer nothing below it can override: a
// dangerous command, or a tool reaching for the rule files themselves.
func TestCheckBlacklist(t *testing.T) {
	modes := []Mode{ModeDefault, ModeAcceptEdits, ModeBypass}

	t.Run("dangerous commands are denied in every mode", func(t *testing.T) {
		for _, mode := range modes {
			checker, _ := newTestChecker(t, mode)
			got := checker.Check(bashTool, map[string]any{"command": "rm -rf /"})
			if got.Effect != Deny {
				t.Fatalf("mode %s: rm -rf / = %s, want deny", mode, got.Effect)
			}
			if got.Reason == "" {
				t.Fatalf("mode %s: deny carries no reason", mode)
			}
		}
	})

	t.Run("rule files are protected from tools", func(t *testing.T) {
		for _, mode := range modes {
			checker, work := newTestChecker(t, mode)
			local := filepath.Join(work, ".aethercode", "permissions.local.yaml")

			if got := checker.Check(writeTool, map[string]any{"file_path": local}); got.Effect != Deny {
				t.Fatalf("mode %s: Write to the rule file = %s, want deny", mode, got.Effect)
			}
			if got := checker.Check(editTool, map[string]any{"file_path": ".aethercode/permissions.yaml"}); got.Effect != Deny {
				t.Fatalf("mode %s: Edit of the rule file = %s, want deny", mode, got.Effect)
			}
			if got := checker.Check(bashTool, map[string]any{"command": "echo x >> .aethercode/permissions.yaml"}); got.Effect != Deny {
				t.Fatalf("mode %s: bash append to the rule file = %s, want deny", mode, got.Effect)
			}
			// Reading them is not what the protection is about.
			if got := checker.Check(readTool, map[string]any{"file_path": local}); got.Effect != Allow {
				t.Fatalf("mode %s: Read of the rule file = %s, want allow", mode, got.Effect)
			}
		}
	})

}

// TestCheckBoundary covers the workspace fallback: a file path outside it asks
// unless a rule (an outright absolute-path one, or the session's own grant)
// admits it first, and bypass mode lets it through.
func TestCheckBoundary(t *testing.T) {
	modes := []Mode{ModeDefault, ModeAcceptEdits, ModeBypass}

	t.Run("outside paths ask, except in bypass", func(t *testing.T) {
		for _, mode := range modes {
			checker, _ := newTestChecker(t, mode)
			got := checker.Check(readTool, map[string]any{"file_path": "/etc/hosts"})
			want := Ask
			if mode == ModeBypass {
				want = Allow
			}
			if got.Effect != want {
				t.Fatalf("mode %s: out-of-bounds read = %s, want %s", mode, got.Effect, want)
			}
		}
	})

	t.Run("an absolute-path rule admits an outside path", func(t *testing.T) {
		checker, work := newTestChecker(t, ModeDefault)
		writeLocalRules(t, work, `rules:
  - rule: Read(/etc/**)
    effect: allow
`)
		if got := checker.Check(readTool, map[string]any{"file_path": "/etc/hosts"}); got.Effect != Allow {
			t.Fatalf("read under an admitted path = %s, want allow", got.Effect)
		}
		if got := checker.Check(readTool, map[string]any{"file_path": "/etc/passwd"}); got.Effect != Allow {
			t.Fatalf("another file in the same directory = %s, want allow", got.Effect)
		}
		if got := checker.Check(readTool, map[string]any{"file_path": "/etc/shadow-"}); got.Effect != Allow {
			t.Fatalf("a file under the admitted directory = %s, want allow", got.Effect)
		}
		if got := checker.Check(readTool, map[string]any{"file_path": "/var/log/syslog"}); got.Effect != Ask {
			t.Fatalf("a path outside the admitted one = %s, want ask", got.Effect)
		}
	})

	t.Run("an absolute-path rule denies an outside path", func(t *testing.T) {
		checker, work := newTestChecker(t, ModeBypass)
		writeLocalRules(t, work, `rules:
  - rule: Read(/etc/shadow)
    effect: deny
`)
		if got := checker.Check(readTool, map[string]any{"file_path": "/etc/shadow"}); got.Effect != Deny {
			t.Fatalf("denied path in bypass mode = %s, want deny", got.Effect)
		}
	})

	t.Run("a symlink leaving the workspace is outside", func(t *testing.T) {
		if _, err := os.Stat("/etc/hosts"); err != nil {
			t.Skipf("no /etc/hosts to point at: %v", err)
		}
		checker, work := newTestChecker(t, ModeDefault)
		link := filepath.Join(work, "etc-link")
		if err := os.Symlink("/etc", link); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}
		if got := checker.Check(readTool, map[string]any{"file_path": filepath.Join(link, "hosts")}); got.Effect != Ask {
			t.Fatalf("read through a link that leaves = %s, want ask", got.Effect)
		}
	})
}

func TestPathPattern(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := pathPattern(dir); got != dir+"/**" {
		t.Fatalf("pathPattern(directory) = %q, want %q", got, dir+"/**")
	}
	if got := pathPattern(file); got != file {
		t.Fatalf("pathPattern(file) = %q, want %q", got, file)
	}
	// A path that does not exist yet is a file it will become.
	if got := pathPattern(filepath.Join(dir, "new.txt")); got != filepath.Join(dir, "new.txt") {
		t.Fatalf("pathPattern(missing) = %q", got)
	}
}

func TestResolvePath(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "real")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	// An existing link resolves to its target.
	got, err := resolvePath(link)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Fatalf("resolvePath(link) = %q, want %q", got, target)
	}

	// A missing tail resolves through its deepest existing ancestor, so a new
	// file under a linked directory lands where the link points.
	got, err = resolvePath(filepath.Join(link, "sub", "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(target, "sub", "new.txt"); got != want {
		t.Fatalf("resolvePath(link/sub/new.txt) = %q, want %q", got, want)
	}

	// A path where nothing exists is still answered, not panicked on.
	if _, err := resolvePath(filepath.Join(base, "no", "such", "path")); err != nil {
		t.Fatalf("resolvePath on a missing tree: %v", err)
	}
}

func TestWithin(t *testing.T) {
	tests := []struct {
		path   string
		root   string
		inside bool
	}{
		{"/a/proj", "/a/proj", true},
		{"/a/proj/src/a.go", "/a/proj", true},
		{"/a/proj-evil/x", "/a/proj", false},
		{"/a/other/x", "/a/proj", false},
	}
	for _, tt := range tests {
		if got := within(tt.path, tt.root); got != tt.inside {
			t.Fatalf("within(%q, %q) = %v, want %v", tt.path, tt.root, got, tt.inside)
		}
	}
}

// TestCheckRules covers the rule channel: subcommand semantics for commands,
// the session rules that live alongside the file rules, and the mode matrix
// behind it all.
func TestCheckRules(t *testing.T) {
	t.Run("an allow rule admits the command", func(t *testing.T) {
		checker, work := newTestChecker(t, ModeDefault)
		writeLocalRules(t, work, "rules:\n  - rule: Bash(git *)\n    effect: allow\n")
		if got := checker.Check(bashTool, map[string]any{"command": "git status"}); got.Effect != Allow {
			t.Fatalf("git status with Bash(git *) allow = %s, want allow", got.Effect)
		}
	})

	t.Run("an unmatched subcommand falls through to the mode", func(t *testing.T) {
		checker, work := newTestChecker(t, ModeDefault)
		writeLocalRules(t, work, "rules:\n  - rule: Bash(ls *)\n    effect: allow\n")
		if got := checker.Check(bashTool, map[string]any{"command": "ls && rm -rf build"}); got.Effect != Ask {
			t.Fatalf("ls && rm -rf build with Bash(ls *) allow = %s, want ask", got.Effect)
		}
	})

	t.Run("every subcommand allowed means allowed", func(t *testing.T) {
		checker, work := newTestChecker(t, ModeDefault)
		writeLocalRules(t, work, `rules:
  - rule: Bash(ls *)
    effect: allow
  - rule: Bash(rm -rf build)
    effect: allow
`)
		if got := checker.Check(bashTool, map[string]any{"command": "ls -la && rm -rf build"}); got.Effect != Allow {
			t.Fatalf("all subcommands allowed = %s, want allow", got.Effect)
		}
	})

	t.Run("a deny rule outranks an allow rule", func(t *testing.T) {
		checker, work := newTestChecker(t, ModeDefault)
		writeLocalRules(t, work, `rules:
  - rule: Bash(rm *)
    effect: allow
  - rule: Bash(rm -rf build)
    effect: deny
`)
		if got := checker.Check(bashTool, map[string]any{"command": "rm -rf build"}); got.Effect != Deny {
			t.Fatalf("deny rule vs allow rule = %s, want deny", got.Effect)
		}
	})

	t.Run("session grants admit without touching a file", func(t *testing.T) {
		checker, _ := newTestChecker(t, ModeDefault)
		if got := checker.Check(bashTool, map[string]any{"command": "git status"}); got.Effect != Ask {
			t.Fatalf("git status before the grant = %s, want ask", got.Effect)
		}
		checker.AddSessionRule(Rule{Tool: "Bash", Pattern: "git status", Effect: Allow})
		if got := checker.Check(bashTool, map[string]any{"command": "git status"}); got.Effect != Allow {
			t.Fatalf("git status after the grant = %s, want allow", got.Effect)
		}
	})

	t.Run("the mode matrix decides what no rule covers", func(t *testing.T) {
		checker, work := newTestChecker(t, ModeDefault)
		target := map[string]any{"file_path": filepath.Join(work, "notes.md")}
		if got := checker.Check(writeTool, target); got.Effect != Ask {
			t.Fatalf("default: in-project write = %s, want ask", got.Effect)
		}
		checker.SetMode(ModeAcceptEdits)
		if got := checker.Check(writeTool, target); got.Effect != Allow {
			t.Fatalf("acceptEdits: in-project write = %s, want allow", got.Effect)
		}
		if got := checker.Check(bashTool, map[string]any{"command": "go test ./..."}); got.Effect != Ask {
			t.Fatalf("acceptEdits: command = %s, want ask", got.Effect)
		}
		checker.SetMode(ModeBypass)
		if got := checker.Check(bashTool, map[string]any{"command": "go test ./..."}); got.Effect != Allow {
			t.Fatalf("bypass: command = %s, want allow", got.Effect)
		}
	})
}

func TestSuggestedRules(t *testing.T) {
	t.Run("a file outside the workspace grants just that file", func(t *testing.T) {
		checker, _ := newTestChecker(t, ModeDefault)
		rules := checker.SuggestedRules(readTool, map[string]any{"file_path": "/etc/hosts"})
		if len(rules) != 1 || rules[0].Pattern != "/etc/hosts" {
			t.Fatalf("rules = %+v, want one rule for /etc/hosts", rules)
		}

		// The granted rule admits the file and nothing beside it.
		checker.AddSessionRule(rules[0])
		if got := checker.Check(readTool, map[string]any{"file_path": "/etc/hosts"}); got.Effect != Allow {
			t.Fatalf("after the grant = %s, want allow", got.Effect)
		}
		if got := checker.Check(readTool, map[string]any{"file_path": "/etc/shadow"}); got.Effect != Ask {
			t.Fatalf("a neighbouring file = %s, want ask", got.Effect)
		}
	})

	t.Run("a directory outside the workspace grants its subtree", func(t *testing.T) {
		checker, _ := newTestChecker(t, ModeDefault)
		rules := checker.SuggestedRules(globTool, map[string]any{"pattern": "**/*.go", "path": "/etc"})
		if len(rules) != 1 || rules[0].Pattern != "/etc/**" {
			t.Fatalf("rules = %+v, want one rule for /etc/**", rules)
		}

		checker.AddSessionRule(rules[0])
		if got := checker.Check(globTool, map[string]any{"pattern": "**/*.go", "path": "/etc/ssl"}); got.Effect != Allow {
			t.Fatalf("a subtree search = %s, want allow", got.Effect)
		}
		if got := checker.Check(globTool, map[string]any{"pattern": "**/*.go", "path": "/"}); got.Effect != Ask {
			t.Fatalf("the parent directory = %s, want ask", got.Effect)
		}
	})

	t.Run("a compound command grants one rule per subcommand", func(t *testing.T) {
		checker, _ := newTestChecker(t, ModeDefault)
		rules := checker.SuggestedRules(bashTool, map[string]any{"command": "ls && rm -rf build"})
		if len(rules) != 2 || rules[0].String() != "Bash(ls)" || rules[1].String() != "Bash(rm -rf build)" {
			t.Fatalf("rules = %+v, want Bash(ls) and Bash(rm -rf build)", rules)
		}

		for _, rule := range rules {
			checker.AddSessionRule(rule)
		}
		if got := checker.Check(bashTool, map[string]any{"command": "ls && rm -rf build"}); got.Effect != Allow {
			t.Fatalf("after the grants = %s, want allow", got.Effect)
		}
	})

	t.Run("a file write grants a prefix rule", func(t *testing.T) {
		checker, work := newTestChecker(t, ModeDefault)
		t.Chdir(work)
		rules := checker.SuggestedRules(writeTool, map[string]any{"file_path": "notes.md"})
		if len(rules) != 1 || rules[0].Tool != "Write" || rules[0].Pattern != "notes.md*" {
			t.Fatalf("rules = %+v, want Write(notes.md*)", rules)
		}

		// A long path is cut to the grant length, and the cut pattern still
		// matches the call it came from.
		long := strings.Repeat("nested/", 20) + "file.go"
		rules = checker.SuggestedRules(writeTool, map[string]any{"file_path": long})
		if len(rules) != 1 {
			t.Fatalf("long path rules = %+v, want one", rules)
		}
		if got := rules[0].Pattern; len([]rune(got)) != grantPatternLen+1 || !rules[0].Matches("Write", long) {
			t.Fatalf("grant pattern = %q, want a %d-character prefix that still matches", got, grantPatternLen)
		}
	})

	t.Run("a permanent rule is written to the local file", func(t *testing.T) {
		checker, work := newTestChecker(t, ModeDefault)
		rule := Rule{Tool: "Bash", Pattern: "git status", Effect: Allow}
		if err := checker.AddPermanentRule(rule); err != nil {
			t.Fatalf("AddPermanentRule: %v", err)
		}
		body, err := os.ReadFile(filepath.Join(work, ".aethercode", "permissions.local.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if got := string(body); !strings.Contains(got, "Bash(git status)") {
			t.Fatalf("local file = %q, want it to contain the rule", got)
		}
		if got := checker.Check(bashTool, map[string]any{"command": "git status"}); got.Effect != Allow {
			t.Fatalf("after the permanent rule = %s, want allow", got.Effect)
		}
	})
}
