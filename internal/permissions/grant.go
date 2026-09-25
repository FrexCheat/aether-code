package permissions

import (
	"errors"

	"github.com/FrexCheat/aether-code/internal/tools"
)

// grantPatternLen bounds how much of a call's argument an allow rule keeps.
const grantPatternLen = 60

type Response int

const (
	NoAnswer       Response = iota // nothing came back; nothing is granted
	AllowOnce                      // run this call; ask again next time
	AllowSession                   // record a grant until the process exits
	AllowPermanent                 // write the grant to the local rule file
	Reject                         // do not run this call
)

type Request struct {
	ToolName string
	Action   string
	Rules    []Rule
	Reply    chan<- Response
}

func (r Response) Allows() bool {
	switch r {
	case AllowOnce, AllowSession, AllowPermanent:
		return true
	default:
		return false
	}
}

// Question builds the prompt the TUI shows for a call the ladder left open.
func (c *Checker) Question(tool tools.Tool, args map[string]any) Request {
	if tool == nil {
		return Request{}
	}
	return Request{
		ToolName: tool.Name(),
		Action:   ruleValue(specOf(tool), args),
		Rules:    c.SuggestedRules(tool, args),
	}
}

func (c *Checker) SuggestedRules(tool tools.Tool, args map[string]any) []Rule {
	if tool == nil {
		return nil
	}
	name := tool.Name()
	spec := specOf(tool)
	argument := ruleValue(spec, args)

	if spec != nil && spec.RuleArgIsPath && argument != "" {
		if resolved, inside := c.resolveInWorkspace(argument); !inside {
			return []Rule{{Tool: name, Pattern: rulePatternForPath(resolved), Effect: Allow}}
		}
	}
	if argument == "" {
		return []Rule{{Tool: name, Pattern: "*", Effect: Allow}}
	}
	if tool.Category() == tools.CategoryCommand {
		subcommands := analyzeCommand(argument)
		// A rule the write boundary would refuse to honour is not offered, so
		// the dialog cannot promise a grant the next identical call would ask
		// about again.
		if c.checkWrites(subcommands) != nil {
			return nil
		}
		rules := make([]Rule, 0, len(subcommands))
		for _, sub := range subcommands {
			// A subcommand that runs code its text does not show can never be
			// allowed by a rule, so suggesting one would promise a grant the
			// next identical call would not honour.
			if sub.opaque {
				continue
			}
			rules = append(rules, Rule{Tool: name, Pattern: escapeRulePattern(sub.text), Effect: Allow})
		}
		return rules
	}
	runes := []rune(argument)
	if len(runes) > grantPatternLen {
		runes = runes[:grantPatternLen]
	}
	return []Rule{{Tool: name, Pattern: escapeRulePattern(string(runes)) + "*", Effect: Allow}}
}

func (c *Checker) RecordGrant(rules []Rule, response Response) error {
	switch response {
	case AllowSession:
		for _, rule := range rules {
			c.AddSessionRule(rule)
		}
	case AllowPermanent:
		var failures []error
		for _, rule := range rules {
			if err := c.AddPermanentRule(rule); err != nil {
				failures = append(failures, err)
			}
		}
		return errors.Join(failures...)
	}
	return nil
}

func (c *Checker) AddSessionRule(rule Rule) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionRules = append(c.sessionRules, rule)
}

func (c *Checker) AddPermanentRule(rule Rule) error {
	return c.rules.AppendLocalRule(rule)
}
