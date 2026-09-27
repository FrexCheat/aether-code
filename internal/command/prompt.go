package command

import "strings"

// RenderPrompt expands a prompt command's template. $ARGUMENTS is replaced by
// the raw arguments; when the template carries no placeholder and arguments
// were given, they are appended, so a command's free text is never dropped.
func RenderPrompt(template string, inv Invocation) string {
	if strings.Contains(template, "$ARGUMENTS") {
		return strings.TrimSpace(strings.ReplaceAll(template, "$ARGUMENTS", inv.RawArgs))
	}
	if inv.RawArgs != "" {
		return strings.TrimSpace(template) + "\n\n" + inv.RawArgs
	}
	return strings.TrimSpace(template)
}
