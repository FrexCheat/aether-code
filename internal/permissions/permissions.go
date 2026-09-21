// Package permissions decides whether a tool call may run. Every call walks one
// ladder — blacklist, rules, workspace boundary, mode — and the first layer with
// an answer wins; an ask is turned into a dialog by the agent and the TUI.
package permissions

import (
	"fmt"
	"maps"
	"os"
	"slices"
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
	ModeDefault     Mode = "default"     // reads run; writes and commands ask
	ModeAcceptEdits Mode = "acceptEdits" // writes run too; commands still ask
	ModeBypass      Mode = "bypass"      // everything runs, boundary included
)

type toolSpec struct {
	arg    string
	isPath bool
}

type Checker struct {
	workspace string // resolved working directory
	tempDir   string // resolved temp directory, inside the workspace scope too
	rules     *RuleEngine

	mu           sync.RWMutex
	mode         Mode
	sessionRules []Rule // "allow for this session" rules, process-local
}

var switchableModes = []Mode{ModeDefault, ModeAcceptEdits, ModeBypass}

var toolSpecs = map[string]toolSpec{
	"Bash":  {arg: "command"},
	"Read":  {arg: "file_path", isPath: true},
	"Write": {arg: "file_path", isPath: true},
	"Edit":  {arg: "file_path", isPath: true},
	"Glob":  {arg: "pattern"},
	"Grep":  {arg: "pattern"},
	// ToolSearch is rule-addressable even though it never asks by itself.
	// McpCall is absent on purpose: the agent resolves it to the target MCP
	// tool before the ladder runs, so MCP rules anchor on the tool's own
	// name and no second vocabulary exists.
	"ToolSearch": {arg: "query"},
}

var toolNames = slices.Sorted(maps.Keys(toolSpecs))

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

func (c *Checker) snapshot() ([]Rule, Mode) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rules := make([]Rule, 0, len(c.sessionRules))
	rules = append(rules, c.sessionRules...)
	rules = append(rules, c.rules.Snapshot()...)
	return rules, c.mode
}

func ruleArgument(name string, args map[string]any) string {
	value, _ := args[toolSpecs[name].arg].(string)
	return value
}

// checkBlacklist is L1: hard denials, which no mode, rule or configuration can
// lift. A command is checked whole and again per subcommand — the whole-line
// pass catches a shape that only exists across a separator (`curl … | sh`),
// while the per-subcommand pass names the offending segment of `ls && rm -rf /`.
func checkBlacklist(category tools.Category, argument string, subcommands []subcommand) *Decision {
	if category == tools.CategoryCommand {
		hit, reason := DetectDangerous(argument)
		for _, sub := range subcommands {
			if sub.text == strings.TrimSpace(argument) {
				continue
			}
			if ok, subReason := DetectDangerous(sub.text); ok {
				hit, reason = true, subReason+" — matched subcommand `"+sub.text+"`"
				break
			}
		}
		if hit {
			return &Decision{Effect: Deny, Reason: "Permission denied: " + reason}
		}
	}
	if category == tools.CategoryCommand || category == tools.CategoryWrite {
		if ok, reason := DetectProtectedPath(argument); ok {
			return &Decision{Effect: Deny, Reason: "Permission denied: " + reason}
		}
	}
	return nil
}

// checkRules is L2: the one channel for everything the blacklist did not settle.
// Rules judge paths outside the workspace too, so an explicit absolute-path rule
// admits or denies them. A command is judged subcommand by subcommand: any deny
// denies the line, any ask asks about it, and only a line whose every subcommand
// matched an allow rule is allowed. One unmatched subcommand falls through, so
// `Bash(ls *) allow` cannot carry `ls && rm -rf build` through.
//
// An allow rule is also held back from a subcommand that runs code its text does
// not show — `Bash(echo *)` must not carry `echo $(rm -rf build)` through. Deny
// and ask rules still apply to it, since those only ever tighten the answer.
func checkRules(name, argument string, isCommand bool, subcommands []subcommand, rules []Rule) *Decision {
	if len(rules) == 0 {
		return nil
	}
	if isCommand {
		var ask *Decision
		allAllowed := len(subcommands) > 0
		for _, sub := range subcommands {
			decision := EvaluateRules(rules, name, sub.text)
			switch {
			case decision == nil:
				allAllowed = false
			case decision.Effect == Deny:
				return decision
			case sub.opaque && decision.Effect == Allow:
				allAllowed = false
				if ask == nil {
					ask = &Decision{
						Effect: Ask,
						Reason: "no rule can cover `" + sub.text + "`: it runs a nested command",
					}
				}
			case decision.Effect == Ask:
				allAllowed = false
				if ask == nil {
					ask = decision
				}
			}
		}
		switch {
		case ask != nil:
			return ask
		case allAllowed:
			return &Decision{Effect: Allow, Reason: "allowed by rule: every subcommand matched an allow rule"}
		}
		return nil
	}

	var matched *Decision
	for _, value := range checkPathPattern(name, argument) {
		switch decision := EvaluateRules(rules, name, value); {
		case decision == nil:
		case decision.Effect == Deny:
			return decision
		case matched == nil || matched.Effect == Allow:
			matched = decision
		}
	}
	return matched
}

// modeDecision is L4, deciding only what nothing else covered. Bypass
// decides first: it means every call runs, whatever its category — including
// the unknown categories (mcp) the conservative branch below would ask about.
func modeDecision(mode Mode, category tools.Category) Effect {
	if mode == ModeBypass {
		return Allow
	}
	switch category {
	case tools.CategoryRead, tools.CategoryWrite, tools.CategoryCommand:
	default:
		// An unrecognized category gets the conservative answer.
		return Ask
	}
	switch mode {
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

// checkWrites is the workspace boundary for a command's output redirections.
// A rule that names a command has not named a path, so a redirect riding along
// with it is judged on its own: `Bash(echo *)` must not carry a write to
// anywhere on the disk. The ladder's own boundary cannot do this job — it only
// ever sees tools whose argument is a path, and by then a rule has answered.
func (c *Checker) checkWrites(subcommands []subcommand) *Decision {
	anyRedirect := func(sub subcommand) bool { return len(sub.redirects) > 0 }
	if !slices.ContainsFunc(subcommands, anyRedirect) {
		return nil
	}
	if slices.ContainsFunc(subcommands, func(sub subcommand) bool { return sub.changesDir }) {
		return &Decision{
			Effect: Ask,
			Reason: "a redirection follows a directory change, so the path it writes cannot be resolved",
		}
	}
	for _, sub := range subcommands {
		for _, redir := range sub.redirects {
			if !redir.literal {
				return &Decision{
					Effect: Ask,
					Reason: "a redirection target expands or globs, so the path it writes cannot be checked",
				}
			}
			if resolved, inside := c.resolveInWorkspace(redir.target); !inside {
				return &Decision{Effect: Ask, Reason: "path is outside the workspace: " + resolved}
			}
		}
	}
	return nil
}

func (c *Checker) Check(tool tools.Tool, args map[string]any) Decision {
	if tool == nil {
		return Decision{Effect: Ask, Reason: "unknown tool"}
	}
	name := tool.Name()
	category := tool.Category()
	isCommand := category == tools.CategoryCommand
	argument := ruleArgument(name, args)

	var subcommands []subcommand
	if isCommand {
		subcommands = analyzeCommand(argument)
	}

	if decision := checkBlacklist(category, argument, subcommands); decision != nil {
		return *decision
	}
	rules, mode := c.snapshot()
	decision := checkRules(name, argument, isCommand, subcommands, rules)
	// A deny or ask rule outranks the write boundary; an allow rule does not,
	// having named a command rather than the path that command writes.
	if decision != nil && decision.Effect == Allow && isCommand && mode != ModeBypass {
		if writeDecision := c.checkWrites(subcommands); writeDecision != nil {
			return *writeDecision
		}
	}
	if decision != nil {
		return *decision
	}
	if toolSpecs[name].isPath && argument != "" {
		if resolved, inside := c.resolveInWorkspace(argument); !inside && mode != ModeBypass {
			return Decision{Effect: Ask, Reason: "path is outside the workspace: " + resolved}
		}
	}
	if modeDecision(mode, category) == Allow {
		return Decision{Effect: Allow, Reason: "allowed by mode " + string(mode)}
	}
	return Decision{
		Effect: Ask,
		Reason: fmt.Sprintf("mode %s asks for confirmation before this kind of call", mode),
	}
}
