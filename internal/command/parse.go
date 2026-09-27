package command

import (
	"strings"
	"unicode"
)

// Invocation is a parsed slash command line.
type Invocation struct {
	Name    string   // command name, lowercased, without the slash
	Args    []string // whitespace-separated arguments
	RawArgs string   // the arguments verbatim, for prompt templates
}

// Parse splits a submitted line into a command invocation. It reports false
// when the line is not a slash command at all — empty input, no leading slash,
// or a bare "/" with no name. A caller that sees a slash-prefixed line Parse
// rejects should treat it as an unknown command rather than a message.
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
