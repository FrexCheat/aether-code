package command

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode"
)

// Registry holds the commands by every spelling that selects them, in two
// layers: the static one, fixed at startup, and the dynamic one, which the host
// replaces wholesale (the Skill commands, one per loaded Skill). Lookup and
// listing put the static layer first, so a file the user drops in can never take
// a built-in's spelling away from it.
type Registry struct {
	mu       sync.RWMutex
	commands []*Command
	byName   map[string]*Command
	dynamic  []*Command
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

// SetDynamic replaces the dynamic layer and returns the names it had to drop
// because a static command already owns them, so the caller can say so. A name
// claimed twice inside the batch panics, like New's conflict does: that is a
// programming error, not a user's.
func (r *Registry) SetDynamic(commands []*Command) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	fresh := make([]*Command, 0, len(commands))
	claimed := make(map[string]string, len(commands))
	var shadowed []string
	for _, c := range commands {
		if c == nil || c.Name == "" {
			continue
		}
		key, err := normalizeKey(c.Name)
		if err != nil {
			continue
		}
		if _, static := r.byName[key]; static {
			shadowed = append(shadowed, c.Name)
			continue
		}
		if prev, dup := claimed[key]; dup {
			panic(fmt.Sprintf("command: dynamic name %q is claimed by both %q and %q", key, prev, c.Name))
		}
		claimed[key] = c.Name
		fresh = append(fresh, c)
	}
	r.dynamic = fresh
	return shadowed
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

// Lookup resolves a command name or alias, case-insensitively. The dynamic
// layer is scanned rather than indexed: it is small, and it is replaced as a
// whole, so a second map would only be one more thing to keep in step.
func (r *Registry) Lookup(name string) *Command {
	if r == nil {
		return nil
	}
	key, err := normalizeKey(name)
	if err != nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if c, ok := r.byName[key]; ok {
		return c
	}
	for _, c := range r.dynamic {
		if matchesName(c, key) {
			return c
		}
	}
	return nil
}

// All returns the commands in registration order, the static layer first.
func (r *Registry) All() []*Command {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.dynamic) == 0 {
		return r.commands
	}
	return append(slices.Clone(r.commands), r.dynamic...)
}

// Match returns the commands whose name or an alias starts with prefix, in
// registration order. An empty prefix matches every command.
func (r *Registry) Match(prefix string) []*Command {
	if r == nil {
		return nil
	}
	p := strings.ToLower(strings.TrimSpace(prefix))
	r.mu.RLock()
	defer r.mu.RUnlock()
	var matched []*Command
	for _, c := range r.commands {
		if matchesPrefix(c, p) {
			matched = append(matched, c)
		}
	}
	for _, c := range r.dynamic {
		if matchesPrefix(c, p) {
			matched = append(matched, c)
		}
	}
	return matched
}

// matchesName compares a command's spellings against an already-normalized key.
func matchesName(c *Command, key string) bool {
	if normalized, err := normalizeKey(c.Name); err == nil && normalized == key {
		return true
	}
	for _, alias := range c.Aliases {
		normalized, err := normalizeKey(alias)
		if err == nil && normalized == key {
			return true
		}
	}
	return false
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
