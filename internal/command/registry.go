package command

import (
	"fmt"
	"strings"
	"unicode"
)

// Registry holds the commands by every spelling that selects them.
type Registry struct {
	commands []*Command
	byName   map[string]*Command
}

// New builds a registry. It panics on a name or alias that is empty, malformed
// or claimed twice: a conflict is a startup failure, decided before the program
// serves a single line, not a runtime surprise.
func New(commands ...*Command) *Registry {
	r := &Registry{byName: make(map[string]*Command)}
	for _, c := range commands {
		if c == nil {
			panic("command: a registered command is nil")
		}
		if c.Name == "" {
			panic("command: a registered command has no name")
		}
		for _, key := range append([]string{c.Name}, c.Aliases...) {
			normalized, err := normalizeKey(key)
			if err != nil {
				panic(fmt.Sprintf("command: %v (in %q)", err, c.Name))
			}
			if prev, dup := r.byName[normalized]; dup {
				panic(fmt.Sprintf("command: name or alias %q is claimed by both %q and %q", normalized, prev.Name, c.Name))
			}
			r.byName[normalized] = c
		}
		r.commands = append(r.commands, c)
	}
	return r
}

// normalizeKey lowercases a spelling and rejects the shapes a command name may
// not take, so the map is never keyed by something a user could not type.
func normalizeKey(key string) (string, error) {
	k := strings.ToLower(strings.TrimSpace(key))
	switch {
	case k == "":
		return "", fmt.Errorf("empty name or alias")
	case strings.HasPrefix(k, "/"):
		return "", fmt.Errorf("name or alias %q must not start with /", key)
	case strings.IndexFunc(k, unicode.IsSpace) >= 0:
		return "", fmt.Errorf("name or alias %q must not contain whitespace", key)
	}
	return k, nil
}

// Lookup resolves a command name or alias, case-insensitively.
func (r *Registry) Lookup(name string) *Command {
	if r == nil {
		return nil
	}
	return r.byName[strings.ToLower(strings.TrimSpace(name))]
}

// All returns the commands in registration order.
func (r *Registry) All() []*Command {
	if r == nil {
		return nil
	}
	return r.commands
}

// Match returns the commands whose name or an alias starts with prefix, in
// registration order. An empty prefix matches every command.
func (r *Registry) Match(prefix string) []*Command {
	if r == nil {
		return nil
	}
	p := strings.ToLower(strings.TrimSpace(prefix))
	var matched []*Command
	for _, c := range r.commands {
		if matchesPrefix(c, p) {
			matched = append(matched, c)
		}
	}
	return matched
}

func matchesPrefix(c *Command, prefix string) bool {
	if prefix == "" {
		return true
	}
	if strings.HasPrefix(strings.ToLower(c.Name), prefix) {
		return true
	}
	for _, alias := range c.Aliases {
		if strings.HasPrefix(strings.ToLower(alias), prefix) {
			return true
		}
	}
	return false
}
