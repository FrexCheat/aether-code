package permissions

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/FrexCheat/aether-code/internal/tools"
)

func rulePatternForPath(resolved string) string {
	if info, err := os.Stat(resolved); err == nil && info.IsDir() {
		return resolved + "/**"
	}
	return resolved
}

func (c *Checker) resolveInWorkspace(path string) (resolved string, inside bool) {
	resolved, err := resolvePath(path)
	if err != nil {
		return path, false
	}
	if within(resolved, c.workspace) {
		return resolved, true
	}
	if c.tempDir != "" && within(resolved, c.tempDir) {
		return resolved, true
	}
	return resolved, c.withinExtraRoot(resolved)
}

func (c *Checker) withinExtraRoot(path string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, root := range c.extraRoots {
		if within(path, root) {
			return true
		}
	}
	return false
}

func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func resolvePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	var rest string
	current := filepath.Clean(abs)
	for {
		parent := filepath.Dir(current)
		if parent == current {
			// Nothing along the way exists (only on a deep new tree); the
			// cleaned absolute path is the best answer available.
			return filepath.Clean(abs), nil
		}
		rest = filepath.Join(filepath.Base(current), rest)
		current = parent
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, rest), nil
		}
	}
}

// checkPathPattern produces the values a rule is matched against: the raw
// argument, plus the resolved form a recorded grant uses when the argument is
// a path.
func checkPathPattern(spec *tools.Spec, arg string) []string {
	args := []string{arg}
	if spec == nil || !spec.RuleArgIsPath || arg == "" {
		return args
	}
	resolved, err := resolvePath(arg)
	if err != nil {
		return args
	}
	if asRecorded := rulePatternForPath(resolved); asRecorded != arg {
		args = append(args, asRecorded)
	}
	return args
}
