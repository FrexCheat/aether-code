package mcp

import "strings"

const (
	toolPrefix    = "mcp__"
	toolSeparator = "__"
	// maxToolName is the API-side limit on tool names; a longer name would
	// be rejected by the provider and poison every request, not just this
	// tool's calls.
	maxToolName = 64
)

func Sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, s)
}

func Qualified(server, rawTool string) string {
	return toolPrefix + Sanitize(server) + toolSeparator + Sanitize(rawTool)
}

func ParseQualifiedName(name string) (server, tool string, ok bool) {
	rest, ok := strings.CutPrefix(name, toolPrefix)
	if !ok {
		return "", "", false
	}
	head, tail, found := strings.Cut(rest, toolSeparator)
	if !found || head == "" || tail == "" {
		return "", "", false
	}
	return head, tail, true
}
