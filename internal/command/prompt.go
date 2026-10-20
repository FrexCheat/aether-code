package command

import "strings"

// Without $ARGUMENTS, given arguments are appended so free text is not dropped.
func Expand(template, args string) string {
	if strings.Contains(template, "$ARGUMENTS") {
		return strings.TrimSpace(strings.ReplaceAll(template, "$ARGUMENTS", args))
	}
	if args != "" {
		return strings.TrimSpace(template) + "\n\n" + args
	}
	return strings.TrimSpace(template)
}

func RenderPrompt(template string, inv Invocation) string {
	return Expand(template, inv.RawArgs)
}
