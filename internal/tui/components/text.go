package components

import (
	"strings"

	"charm.land/lipgloss/v2"
)

func WrapLines(s string, width int) []string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		if line == "" || width <= 0 {
			out = append(out, line)
			continue
		}
		out = append(out, strings.Split(lipgloss.Wrap(line, width, ""), "\n")...)
	}
	return out
}
