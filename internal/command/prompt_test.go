package command

import "testing"

func TestExpand(t *testing.T) {
	tests := []struct {
		name     string
		template string
		args     string
		want     string
	}{
		{name: "placeholder", template: "审查 $ARGUMENTS 这些改动", args: "src/a.go", want: "审查 src/a.go 这些改动"},
		{name: "placeholder twice", template: "$ARGUMENTS 与 $ARGUMENTS 都看", args: "x", want: "x 与 x 都看"},
		{name: "placeholder with no arguments", template: "审查 $ARGUMENTS 全部改动", args: "", want: "审查  全部改动"},
		{name: "no placeholder appends", template: "照做。", args: "额外要求", want: "照做。\n\n额外要求"},
		{name: "no placeholder and no arguments", template: "  照做。  ", want: "照做。"},
		{name: "arguments are taken verbatim", template: "跑 $ARGUMENTS", args: "a  b", want: "跑 a  b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Expand(tt.template, tt.args); got != tt.want {
				t.Errorf("Expand(%q, %q) = %q, want %q", tt.template, tt.args, got, tt.want)
			}
		})
	}
}

// A prompt command and a Skill body are the same rule on different text.
func TestRenderPromptSharesExpand(t *testing.T) {
	inv := Invocation{Name: "review", Args: []string{"a", "b"}, RawArgs: "a  b"}
	for _, template := range []string{"审查 $ARGUMENTS", "审查全部改动", ""} {
		if got, want := RenderPrompt(template, inv), Expand(template, inv.RawArgs); got != want {
			t.Errorf("RenderPrompt(%q) = %q, Expand = %q", template, got, want)
		}
	}
}
