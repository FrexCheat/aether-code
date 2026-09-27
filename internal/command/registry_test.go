package command

import (
	"slices"
	"testing"
)

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	return New(
		&Command{Name: "exit"},
		&Command{Name: "help", Aliases: []string{"?"}},
		&Command{Name: "session", Aliases: []string{"sessions"}},
	)
}

func TestLookup(t *testing.T) {
	r := testRegistry(t)
	tests := []struct {
		name string
		want string
	}{
		{"exit", "exit"},
		{"EXIT", "exit"},
		{" help ", "help"},
		{"?", "help"},
		{"sessions", "session"},
		{"SESSION", "session"},
		{"missing", ""},
	}
	for _, tt := range tests {
		got := r.Lookup(tt.name)
		name := ""
		if got != nil {
			name = got.Name
		}
		if name != tt.want {
			t.Errorf("Lookup(%q) = %q, want %q", tt.name, name, tt.want)
		}
	}
}

func TestAllKeepsOrder(t *testing.T) {
	r := testRegistry(t)
	var names []string
	for _, c := range r.All() {
		names = append(names, c.Name)
	}
	if want := []string{"exit", "help", "session"}; !slices.Equal(names, want) {
		t.Errorf("All() = %q, want %q", names, want)
	}
}

func TestMatch(t *testing.T) {
	r := testRegistry(t)
	tests := []struct {
		prefix string
		want   []string
	}{
		{"", []string{"exit", "help", "session"}},
		{"s", []string{"session"}},
		{"se", []string{"session"}},
		{"he", []string{"help"}},
		{"?", []string{"help"}},
		{"EX", []string{"exit"}},
		{"zzz", nil},
	}
	for _, tt := range tests {
		var names []string
		for _, c := range r.Match(tt.prefix) {
			names = append(names, c.Name)
		}
		if !slices.Equal(names, tt.want) {
			t.Errorf("Match(%q) = %q, want %q", tt.prefix, names, tt.want)
		}
	}
}

func TestNewPanicsOnConflict(t *testing.T) {
	tests := []struct {
		name     string
		commands []*Command
	}{
		{"duplicate name", []*Command{{Name: "a"}, {Name: "a"}}},
		{"duplicate alias", []*Command{{Name: "a", Aliases: []string{"x"}}, {Name: "b", Aliases: []string{"x"}}}},
		{"alias collides with name", []*Command{{Name: "a"}, {Name: "b", Aliases: []string{"a"}}}},
		{"case-insensitive collision", []*Command{{Name: "a"}, {Name: "A"}}},
		{"empty name", []*Command{{Name: ""}}},
		{"leading slash", []*Command{{Name: "/a"}}},
		{"whitespace in alias", []*Command{{Name: "a", Aliases: []string{"b c"}}}},
		{"nil command", []*Command{nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("New(%s) did not panic", tt.name)
				}
			}()
			New(tt.commands...)
		})
	}
}
