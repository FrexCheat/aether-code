// Package permissions decides whether a tool call may run, and who decides
// when the answer is "not without asking". Every call walks one ladder:
//
//	L1 blacklist  — destructive commands, and the rule files themselves. Hard
//	                denials: no mode, no rule, no configuration lifts them.
//	L2 rules      — the rule files plus this session's grants. One channel for
//	                everything else, paths included: an explicit absolute-path
//	                rule can deny or admit a path outside the workspace.
//	L3 workspace  — the fallback for file paths that leave the workspace: ask,
//	                so a read of someone else's file needs a word from the user
//	                (bypass mode lets it through). Only reached when no rule
//	                spoke.
//	L4 mode       — the mode matrix, for a call neither rules nor the boundary
//	                decided.
//	L5 human      — the agent and the TUI turn an ask into a dialog.
//
// Each layer looks only at its own input, and the first one with an answer
// returns it. The package depends on the standard library, yaml, and
// internal/tools — it knows nothing about the UI or the model, and never
// touches the file system on a tool's behalf.
package permissions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/FrexCheat/aether-code/internal/tools"
)

type Effect string

const (
	Allow Effect = "allow" // execute
	Ask   Effect = "ask"   // suspend and ask the user
	Deny  Effect = "deny"  // never execute
)

type Decision struct {
	Effect Effect
	Reason string
}

type Mode string

const (
	ModeDefault     Mode = "default"     // reads run, writes and commands ask
	ModeAcceptEdits Mode = "acceptEdits" // writes run too, commands still ask
	ModeBypass      Mode = "bypass"      // everything runs, including out-of-bounds paths
)

// switchableModes is the Shift+Tab cycle, in order.
var switchableModes = []Mode{ModeDefault, ModeAcceptEdits, ModeBypass}

type Checker struct {
	workspace string // resolved working directory
	tempDir   string // resolved temp directory, inside the workspace scope too
	rules     *RuleEngine

	mu           sync.RWMutex
	mode         Mode
	sessionRules []Rule // "allow for this session" rules, process-local
}

func NextMode(m Mode) Mode {
	for i, known := range switchableModes {
		if known == m {
			return switchableModes[(i+1)%len(switchableModes)]
		}
	}
	return ModeDefault
}

func (c *Checker) Mode() Mode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mode
}

func (c *Checker) SetMode(m Mode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mode = m
}

// NewChecker builds the workspace scope and rule engine for a working
// directory. A session always starts in the default mode; the mode is a
// per-session choice and is not persisted.
func NewChecker(workDir string) (*Checker, error) {
	workspace, err := resolvePath(workDir)
	if err != nil {
		return nil, err
	}
	c := &Checker{workspace: workspace, rules: NewRuleEngine(workDir), mode: ModeDefault}
	if tempDir, err := resolvePath(os.TempDir()); err == nil {
		c.tempDir = tempDir
	}
	return c, nil
}

func (c *Checker) Check(tool tools.Tool, args map[string]any) Decision {
	if tool == nil {
		return Decision{Effect: Ask, Reason: "unknown tool"}
	}
	if args == nil {
		args = map[string]any{}
	}

	name := tool.Name()
	content := ExtractContent(name, args)
	isCommand := tool.Category() == tools.CategoryCommand

	var subs []string
	if isCommand {
		subs = SplitCompound(content)
	}

	// L1 — hard blocks. A command is checked whole and again per subcommand:
	// the whole-line pass catches a shape that only exists across a separator
	// (`curl … | sh`), while the per-subcommand pass names the offending
	// segment of `ls && rm -rf /`.
	if isCommand {
		hit, reason := DetectDangerous(content)
		for _, sub := range subs {
			if sub == strings.TrimSpace(content) {
				continue
			}
			if ok, subReason := DetectDangerous(sub); ok {
				hit, reason = true, subReason+" — matched subcommand `"+sub+"`"
				break
			}
		}
		if hit {
			return Decision{Effect: Deny, Reason: "Permission denied: " + reason}
		}
		if ok, reason := DetectProtectedPath(content); ok {
			return Decision{Effect: Deny, Reason: "Permission denied: " + reason}
		}
	} else if name == "Write" || name == "Edit" {
		if ok, reason := DetectProtectedPath(content); ok {
			return Decision{Effect: Deny, Reason: "Permission denied: " + reason}
		}
	}

	rules, mode := c.snapshot()

	// L2 — rules, the one channel for everything the blacklist did not settle:
	// they judge paths outside the workspace too, so an explicit absolute-path
	// rule admits or denies them. A command is judged subcommand by subcommand:
	// any deny denies the line, any ask asks about it, and only a line whose
	// every subcommand matched an allow rule is allowed. One unmatched
	// subcommand falls through, so `Bash(ls *) allow` cannot carry
	// `ls && rm -rf build` through.
	if isCommand {
		var ask *Decision
		allMatched := len(subs) > 0
		for _, sub := range subs {
			decision := EvaluateRules(rules, name, sub)
			switch {
			case decision == nil:
				allMatched = false
			case decision.Effect == Deny:
				return *decision
			case decision.Effect == Ask && ask == nil:
				ask = decision
			}
		}
		switch {
		case ask != nil:
			return *ask
		case allMatched:
			return Decision{Effect: Allow, Reason: "allowed by rule: every subcommand matched an allow rule"}
		}
	} else {
		var hit *Decision
		for _, subject := range ruleSubjects(name, args) {
			switch decision := EvaluateRules(rules, name, subject); {
			case decision == nil:
			case decision.Effect == Deny:
				return *decision
			case hit == nil || hit.Effect == Allow:
				hit = decision
			}
		}
		if hit != nil {
			return *hit
		}
	}

	// L3 — the workspace boundary, reached only when no rule spoke: a file
	// path that leaves the workspace needs the user's word (bypass mode lets it
	// through).
	if path, ok := pathArg(name, args); ok {
		if inside, resolved := c.pathInWorkspace(path); !inside && mode != ModeBypass {
			return Decision{Effect: Ask, Reason: "path is outside the workspace: " + resolved}
		}
	}

	// L4 — the mode matrix, deciding only what nothing else covered.
	if matrixEffect(mode, tool.Category()) == Allow {
		return Decision{Effect: Allow, Reason: "allowed by mode " + string(mode)}
	}
	return Decision{
		Effect: Ask,
		Reason: fmt.Sprintf("mode %s asks for confirmation before this kind of call", mode),
	}
}

// matrixEffect is the L4 fallback: what a call that no earlier layer decided
// gets, given the mode and the tool's category.
func matrixEffect(mode Mode, category tools.Category) Effect {
	switch category {
	case tools.CategoryRead, tools.CategoryWrite, tools.CategoryCommand:
	default:
		// An unrecognized category gets the conservative answer.
		return Ask
	}
	switch mode {
	case ModeBypass:
		return Allow
	case ModeAcceptEdits:
		if category == tools.CategoryCommand {
			return Ask
		}
		return Allow
	default: // ModeDefault, and anything unrecognized, behave the same
		if category == tools.CategoryRead {
			return Allow
		}
		return Ask
	}
}

// grantPatternLen bounds how much of a call's content an allow rule keeps.
const grantPatternLen = 60

// SuggestedRules builds what the dialog would record if the user answers
// "always allow". A path outside the workspace is spelled out absolutely —
// that is the only kind of rule that reaches it — with a directory admitting
// its subtree and a file only itself. Everything else gets a rule on the
// argument as it came, cut to the grant length: the cut is a plain truncation,
// since an ellipsis would stop the rule from matching the call it came from.
func (c *Checker) SuggestedRules(tool tools.Tool, args map[string]any) []Rule {
	if tool == nil {
		return nil
	}
	if args == nil {
		args = map[string]any{}
	}
	name := tool.Name()

	if path, ok := pathArg(name, args); ok && path != "" {
		if inside, resolved := c.pathInWorkspace(path); !inside {
			return []Rule{{Tool: name, Pattern: pathPattern(resolved), Effect: Allow}}
		}
	}

	content := ExtractContent(name, args)
	if content == "" {
		return []Rule{{Tool: name, Pattern: "*", Effect: Allow}}
	}
	// Commands are matched subcommand by subcommand, so allowing a compound
	// command writes one rule per subcommand — a pattern for the whole line
	// could never match.
	if tool.Category() == tools.CategoryCommand {
		subs := SplitCompound(content)
		rules := make([]Rule, 0, len(subs))
		for _, sub := range subs {
			rules = append(rules, Rule{Tool: name, Pattern: sub, Effect: Allow})
		}
		return rules
	}
	runes := []rune(content)
	if len(runes) > grantPatternLen {
		runes = runes[:grantPatternLen]
	}
	return []Rule{{Tool: name, Pattern: string(runes) + "*", Effect: Allow}}
}

// AddSessionRule records a rule in memory only; it is gone when the process
// exits and never reaches a file.
func (c *Checker) AddSessionRule(rule Rule) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionRules = append(c.sessionRules, rule)
}

// AddPermanentRule writes a rule to the local rule file. This is the process
// recording the user's own decision, so it does not pass through Check: the
// blacklist's protection of the rule files applies to what tools attempt, not
// to what the user asked for.
func (c *Checker) AddPermanentRule(rule Rule) error {
	return c.rules.AppendLocalRule(rule)
}

// snapshot returns the rule set and mode of this moment. Session grants come
// first; precedence between rules is by effect, not by position, so order only
// matters between equal effects.
func (c *Checker) snapshot() ([]Rule, Mode) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rules := make([]Rule, 0, len(c.sessionRules))
	rules = append(rules, c.sessionRules...)
	rules = append(rules, c.rules.Snapshot()...)
	return rules, c.mode
}

// ruleSubjects are the strings this call's rules are matched against: the
// argument as written (a path, a command, or a search pattern) and, for the
// tools that carry a path, that path in the spelling a rule would use — so a
// hand-written rule and a dialog grant both land on what the call touches.
func ruleSubjects(name string, args map[string]any) []string {
	content := ExtractContent(name, args)
	subjects := []string{content}
	if path, ok := pathArg(name, args); ok && path != "" {
		if resolved, err := resolvePath(path); err == nil {
			if pattern := pathPattern(resolved); pattern != content {
				subjects = append(subjects, pattern)
			}
		}
	}
	return subjects
}

// pathArg returns the path a file tool works on: the file path for
// read/write/edit, the search root for glob/grep. An empty glob/grep path is
// the working directory, which is inside the workspace by definition.
func pathArg(toolName string, args map[string]any) (string, bool) {
	switch toolName {
	case "Read", "Write", "Edit":
		path, _ := args["file_path"].(string)
		if path == "" {
			return "", false
		}
		return path, true
	case "Glob", "Grep":
		path, _ := args["path"].(string)
		return path, true
	}
	return "", false
}

// pathPattern is how a path is spelled in a rule: a directory admits itself
// and everything under it, a file only itself.
func pathPattern(resolved string) string {
	if info, err := os.Stat(resolved); err == nil && info.IsDir() {
		return resolved + "/**"
	}
	return resolved
}

// pathInWorkspace reports whether a path stays inside the workspace — the
// working directory or the system temp directory — along with the resolved
// path itself. Nothing here reads the rules: they have already had their say
// by the time this is asked.
func (c *Checker) pathInWorkspace(path string) (bool, string) {
	resolved, err := resolvePath(path)
	if err != nil {
		return false, path
	}
	if within(resolved, c.workspace) || (c.tempDir != "" && within(resolved, c.tempDir)) {
		return true, resolved
	}
	return false, resolved
}

// within reports whether path is root itself or below it. Comparing against
// root plus a separator rather than a bare prefix keeps /a/proj-evil from
// passing as a child of /a/proj.
func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// resolvePath returns the absolute path with symbolic links resolved. When the
// path does not exist yet (a file about to be created), the deepest existing
// ancestor is resolved and the remaining segments are appended, so a symlinked
// directory cannot smuggle a write out of the workspace.
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
