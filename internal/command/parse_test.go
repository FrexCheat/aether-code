package command

import (
	"slices"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantOK bool
		want   Invocation
	}{
		{"empty", "", false, Invocation{}},
		{"blank", "   \t ", false, Invocation{}},
		{"plain text", "hello world", false, Invocation{}},
		{"bare slash", "/", false, Invocation{}},
		{"name only", "/exit", true, Invocation{Name: "exit"}},
		{"name lowercased", "/EXIT", true, Invocation{Name: "exit"}},
		{"surrounding spaces", "  /clear  ", true, Invocation{Name: "clear"}},
		{"one arg", "/session list", true, Invocation{Name: "session", Args: []string{"list"}, RawArgs: "list"}},
		{"arg with slash", "/review a/b.go", true, Invocation{Name: "review", Args: []string{"a/b.go"}, RawArgs: "a/b.go"}},
		{"collapsing gaps but raw kept", "/review  a  b ", true, Invocation{Name: "review", Args: []string{"a", "b"}, RawArgs: "a  b"}},
		{"tab separator", "/session\tlist", true, Invocation{Name: "session", Args: []string{"list"}, RawArgs: "list"}},
		{"unknown name still parses", "/nope", true, Invocation{Name: "nope"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Parse(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("Parse(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.Name != tt.want.Name {
				t.Errorf("Name = %q, want %q", got.Name, tt.want.Name)
			}
			if !slices.Equal(got.Args, tt.want.Args) {
				t.Errorf("Args = %q, want %q", got.Args, tt.want.Args)
			}
			if got.RawArgs != tt.want.RawArgs {
				t.Errorf("RawArgs = %q, want %q", got.RawArgs, tt.want.RawArgs)
			}
		})
	}
}

func TestRenderPrompt(t *testing.T) {
	tests := []struct {
		name     string
		template string
		inv      Invocation
		want     string
	}{
		{"substitutes", "review $ARGUMENTS now", Invocation{RawArgs: "a b"}, "review a b now"},
		{"substitutes empty", "scope: $ARGUMENTS", Invocation{}, "scope:"},
		{"appends without placeholder", "do it", Invocation{RawArgs: "a b"}, "do it\n\na b"},
		{"no args no append", "do it", Invocation{}, "do it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RenderPrompt(tt.template, tt.inv); got != tt.want {
				t.Errorf("RenderPrompt(%q, %+v) = %q, want %q", tt.template, tt.inv, got, tt.want)
			}
		})
	}
}
