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

// skillCommands is the shape a host builds for the dynamic layer: one command
// per loaded Skill, carrying no handler of its own.
func skillCommands(names ...string) []*Command {
	commands := make([]*Command, len(names))
	for i, name := range names {
		commands[i] = &Command{Name: name, Description: name, Kind: Skill}
	}
	return commands
}

func TestRegistryDynamic(t *testing.T) {
	r := testRegistry(t)
	r.SetDynamic(skillCommands("commit", "report"))

	if got := r.Lookup("COMMIT"); got == nil || got.Kind != Skill || got.Name != "commit" {
		t.Errorf("Lookup(COMMIT) = %+v, want the commit Skill", got)
	}
	want := []string{"exit", "help", "session", "commit", "report"}
	var names []string
	for _, c := range r.All() {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, want) {
		t.Errorf("All() = %q, want the static layer first", names)
	}
	if got := r.Match("c"); len(got) != 1 || got[0].Name != "commit" {
		t.Errorf("Match(c) = %+v, want the commit Skill", got)
	}

	// The layer is replaced wholesale, not added to.
	r.SetDynamic(skillCommands("report"))
	if r.Lookup("commit") != nil {
		t.Error("a name from the replaced layer is still reachable")
	}
	// And it never takes a built-in's spelling away: the dynamic command is the
	// one that is dropped, and the caller is told which.
	if dropped := r.SetDynamic(skillCommands("help", "report")); !slices.Equal(dropped, []string{"help"}) {
		t.Errorf("SetDynamic() dropped %q, want just the shadowed name", dropped)
	}
	if got := r.Lookup("help"); got == nil || got.Kind != Local {
		t.Errorf("Lookup(help) = %+v, want the built-in to keep its name", got)
	}
	if got := r.Match("hel"); len(got) != 1 || got[0].Kind != Local {
		t.Errorf("Match(hel) = %+v, want only the built-in", got)
	}
	if got := r.Lookup("report"); got == nil || got.Kind != Skill {
		t.Errorf("Lookup(report) = %+v, want the Skill beside it", got)
	}

	defer func() {
		if recover() == nil {
			t.Error("a duplicate inside one batch is a programming error and must panic")
		}
	}()
	r.SetDynamic(skillCommands("dup", "dup"))
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
