package command

import (
	"strings"
	"unicode"
)

type Invocation struct {
	Name    string   // command name, lowercased, without the slash
	Args    []string // whitespace-separated arguments
	RawArgs string   // the arguments verbatim, for prompt templates
}

// Parse reports false for anything that is not a named slash command.
func Parse(input string) (Invocation, bool) {
	s := strings.TrimSpace(input)
	if s == "" || !strings.HasPrefix(s, "/") {
		return Invocation{}, false
	}
	body := s[1:]
	name, rest := body, ""
	if i := strings.IndexFunc(body, unicode.IsSpace); i >= 0 {
		name, rest = body[:i], body[i:]
	}
	name = strings.ToLower(name)
	if name == "" {
		return Invocation{}, false
	}
	rest = strings.TrimSpace(rest)
	return Invocation{Name: name, Args: strings.Fields(rest), RawArgs: rest}, true
}
