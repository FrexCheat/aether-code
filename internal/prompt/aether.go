package prompt

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	aetherMaxFile  = 24 * 1024
	aetherMaxChain = 48 * 1024
)

// projectInstructionsSection carries the AETHER.md chain at the top of the
// prompt, ahead of the generic sections: project instructions outrank the
// defaults. An empty chain yields an empty string, dropped by the assembler.
func projectInstructionsSection(cwd string) string {
	chain := loadAetherChain(cwd)
	if chain == "" {
		return ""
	}
	return "## 项目指令\n" +
		"以下是本仓库的 AETHER.md 指令链（从仓库根目录到当前工作目录逐级排列，越靠后越具体、发生冲突时以靠后者为准）：\n\n" +
		chain
}

// loadAetherChain concatenates the AETHER.md files from the repository root
// down to the working directory — the closest file last, so it wins by
// position — each labelled with its path and capped, with a total budget.
// Outside a repository only the working directory itself is consulted. There
// is no user-level file: instructions are project-scoped by design.
func loadAetherChain(cwd string) string {
	var b strings.Builder
	remaining := aetherMaxChain
	for _, dir := range aetherDirs(cwd) {
		if remaining <= 0 {
			break
		}
		path := filepath.Join(dir, "AETHER.md")
		data, err := os.ReadFile(path)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			continue
		}
		text := truncateBytes(string(data), min(aetherMaxFile, remaining))
		if text == "" {
			continue
		}
		entry := "以下来自 " + path + "：\n\n" + text + "\n\n"
		remaining -= len(entry)
		b.WriteString(entry)
	}
	return strings.TrimSpace(b.String())
}

// aetherDirs lists the directories from the git root (or cwd, outside a
// repository) down to cwd.
func aetherDirs(cwd string) []string {
	root := gitRoot(cwd)
	if root == "" || !within(cwd, root) {
		return []string{cwd}
	}
	var dirs []string
	for current := cwd; ; {
		dirs = append(dirs, current)
		if current == root {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	slices.Reverse(dirs)
	return dirs
}

// gitRoot walks up until it finds a .git entry; "" when there is none. No git
// process is spawned — a worktree's .git file counts the same as a
// directory's.
func gitRoot(dir string) string {
	for current := dir; ; {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// truncateBytes cuts to a byte budget without splitting a rune, and marks the
// cut so the model knows the text continues.
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n… (truncated)"
}
