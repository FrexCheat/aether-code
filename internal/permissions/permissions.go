// Package permissions decides whether a tool call may run.
package permissions

import (
	"encoding/json"
	"fmt"
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

type Checker struct {
	workspace string // resolved working directory
	tempDir   string // resolved temp directory, inside the workspace scope too
	rules     *RuleEngine

	mu           sync.RWMutex
	mode         Mode
	sessionRules []Rule   // "allow for this session" rules, process-local
	extraRoots   []string // resolved roots beyond the workspace the boundary admits
}

var switchableModes = []Mode{ModeDefault, ModeAcceptEdits, ModeBypass}

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

// AddAllowedRoot marks a directory as inside the workspace scope: paths under
// it no longer ask at the boundary. The blacklist and the rules still apply —
// this only extends L3, it never grants on its own.
func (c *Checker) AddAllowedRoot(root string) {
	resolved, err := resolvePath(root)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.extraRoots = append(c.extraRoots, resolved)
}

// RuleWarnings reports rule files that could not be parsed, for the startup
// notice: a user whose rules silently vanished must be told.
func (c *Checker) RuleWarnings() []string {
	return c.rules.Warnings()
}

func (c *Checker) snapshot() ([]Rule, Mode) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rules := make([]Rule, 0, len(c.sessionRules))
	rules = append(rules, c.sessionRules...)
	rules = append(rules, c.rules.Snapshot()...)
	return rules, c.mode
}

// specOf returns the tool's declared Spec, or nil for tools without one
// (MCP wrappers: their rules anchor on the qualified name and the encoded
// arguments).
func specOf(tool tools.Tool) *tools.Spec {
	specified, ok := tool.(tools.Specified)
	if !ok {
		return nil
	}
	spec := specified.Spec()
	return &spec
}

// ruleValue is the string a rule pattern is matched against: the declared
// RuleArg's value when the tool has one, otherwise the JSON-encoded
// arguments — map keys sort, so the encoding is stable — which lets rules
// and grants address MCP arguments. An argument-less call stays "", the
// name-only form rules had before arguments were addressable.
func ruleValue(spec *tools.Spec, args map[string]any) string {
	if spec == nil {
		if len(args) == 0 {
			return ""
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
	value, _ := args[spec.RuleArg].(string)
	return value
}

// The whole-line pass catches shapes that only exist across a separator.
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

// Every subcommand must match an allow rule before the whole line is allowed.
func checkRules(name, argument string, isCommand bool, subcommands []subcommand, spec *tools.Spec, rules []Rule) *Decision {
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
	for _, value := range checkPathPattern(spec, argument) {
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

// A command rule cannot authorize an output redirection to an unchecked path.
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
	spec := specOf(tool)
	argument := ruleValue(spec, args)

	var subcommands []subcommand
	if isCommand {
		subcommands = analyzeCommand(argument)
	}

	if decision := checkBlacklist(category, argument, subcommands); decision != nil {
		return *decision
	}
	rules, mode := c.snapshot()
	decision := checkRules(name, argument, isCommand, subcommands, spec, rules)
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
	// The workspace boundary resolves BoundaryArg, which is not always the
	// argument rules match: Glob and Grep are ruled by their pattern but may
	// carry a path to run in.
	if spec != nil && spec.BoundaryArg != "" && mode != ModeBypass {
		if path, _ := args[spec.BoundaryArg].(string); path != "" {
			if resolved, inside := c.resolveInWorkspace(path); !inside {
				return Decision{Effect: Ask, Reason: "path is outside the workspace: " + resolved}
			}
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
