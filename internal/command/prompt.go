package command

import "strings"

// Expand substitutes a template's arguments. $ARGUMENTS is replaced by the raw
// arguments; when the template carries no placeholder and arguments were given,
// they are appended, so free text is never dropped. Prompt commands and Skill
// bodies share this rule rather than each having their own.
func Expand(template, args string) string {
	if strings.Contains(template, "$ARGUMENTS") {
		return strings.TrimSpace(strings.ReplaceAll(template, "$ARGUMENTS", args))
	}
	if args != "" {
		return strings.TrimSpace(template) + "\n\n" + args
	}
	return strings.TrimSpace(template)
}

// RenderPrompt expands a prompt command's template.
func RenderPrompt(template string, inv Invocation) string {
	return Expand(template, inv.RawArgs)
}
