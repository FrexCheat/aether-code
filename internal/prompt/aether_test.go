package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAether(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AETHER.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAetherChainOrder(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	deep := filepath.Join(sub, "deep")
	writeAether(t, root, "root instructions")
	writeAether(t, sub, "sub instructions")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	chain := loadAetherChain(deep)
	rootAt := strings.Index(chain, "root instructions")
	subAt := strings.Index(chain, "sub instructions")
	if rootAt < 0 || subAt < 0 {
		t.Fatalf("chain is missing entries:\n%s", chain)
	}
	if rootAt > subAt {
		t.Errorf("the repo-root entry must precede the closer entry:\n%s", chain)
	}
	if !strings.Contains(chain, filepath.Join(root, "AETHER.md")) ||
		!strings.Contains(chain, filepath.Join(sub, "AETHER.md")) {
		t.Errorf("entries must be labelled with their paths:\n%s", chain)
	}
}

func TestLoadAetherChainWorktreeGitFile(t *testing.T) {
	root := t.TempDir()
	// A worktree spells .git as a file; the walker only stats the entry.
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	writeAether(t, root, "root instructions")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	if chain := loadAetherChain(sub); !strings.Contains(chain, "root instructions") {
		t.Errorf("a .git file must count as a repository root:\n%s", chain)
	}
}

func TestLoadAetherChainOutsideRepo(t *testing.T) {
	dir := t.TempDir()
	if gitRoot(dir) != "" {
		t.Skip("temp dir lives inside a repository; the fallback cannot be tested here")
	}
	writeAether(t, dir, "only cwd")
	parent := filepath.Dir(dir)
	writeAether(t, parent, "parent must not appear")

	chain := loadAetherChain(dir)
	if !strings.Contains(chain, "only cwd") {
		t.Fatalf("cwd instructions missing:\n%s", chain)
	}
	if strings.Contains(chain, "parent must not appear") {
		t.Errorf("outside a repository only cwd is consulted:\n%s", chain)
	}
}

func TestLoadAetherChainSkipsBlankAndMissing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	writeAether(t, root, "   \n\t\n")
	writeAether(t, sub, "real instructions")

	chain := loadAetherChain(sub)
	if !strings.Contains(chain, "real instructions") {
		t.Fatalf("non-blank entry missing:\n%s", chain)
	}
	if strings.Contains(chain, filepath.Join(root, "AETHER.md")) {
		t.Errorf("blank files must be skipped:\n%s", chain)
	}
}

func TestLoadAetherChainPerFileCap(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	filler := strings.Repeat("x", aetherMaxFile+4096)
	writeAether(t, root, filler)

	chain := loadAetherChain(root)
	if !strings.Contains(chain, "\n… (truncated)") {
		t.Errorf("an oversized file must carry the truncation marker")
	}
	if strings.Contains(chain, filler) {
		t.Errorf("the full oversized file must not survive")
	}
}

func TestLoadAetherChainTotalBudget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Three 20KB files across three levels: the first two fit the 48KB chain
	// budget, the deepest one gets truncated to what remains, and the walk
	// must not overshoot into a negative budget.
	filler := func(c string) string { return strings.Repeat(c, 20480) }
	writeAether(t, root, filler("r"))
	mid := filepath.Join(root, "mid")
	writeAether(t, mid, filler("m"))
	leaf := filepath.Join(mid, "leaf")
	writeAether(t, leaf, filler("l"))

	chain := loadAetherChain(leaf)
	if !strings.Contains(chain, filler("r")) || !strings.Contains(chain, filler("m")) {
		t.Errorf("the first entries should fit in full")
	}
	if strings.Contains(chain, filler("l")) {
		t.Errorf("the last entry must be truncated to the remaining budget")
	}
	if !strings.Contains(chain, "\n… (truncated)") {
		t.Errorf("the truncated entry must carry the marker")
	}
	if len(chain) > aetherMaxChain+1024 {
		t.Errorf("chain length %d overshoots the %d budget", len(chain), aetherMaxChain)
	}
}

func TestLoadAetherChainNearZeroBudget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Files sized so each entry leaves a slightly negative remaining budget:
	// the walk must stop cleanly instead of panicking on a negative cap.
	body := strings.Repeat("y", aetherMaxFile)
	for i := range 4 {
		dir := filepath.Join(root, "d"+string(rune('a'+i)))
		writeAether(t, dir, body)
	}
	deepest := filepath.Join(root, "da", "leaf")
	if err := os.MkdirAll(deepest, 0o755); err != nil {
		t.Fatal(err)
	}
	if chain := loadAetherChain(deepest); chain == "" {
		t.Errorf("chain should still carry the entries that fit")
	}
}

func TestProjectInstructionsSection(t *testing.T) {
	if empty := projectInstructionsSection(t.TempDir()); empty != "" {
		t.Errorf("no chain must yield an empty section for the assembler to drop")
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeAether(t, root, "project rule")
	s := projectInstructionsSection(root)
	if !strings.Contains(s, "## 项目指令") || !strings.Contains(s, "project rule") {
		t.Errorf("section content incomplete:\n%s", s)
	}

	prompt := BuildSystemPrompt(Environment{WorkDir: root}, "")
	if !strings.Contains(prompt, "project rule") {
		t.Errorf("the chain must ride the assembled system prompt")
	}
}
