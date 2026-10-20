package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/FrexCheat/aether-code/internal/tools"
)

func toolFixture(t *testing.T) (*Tool, *State) {
	t.Helper()
	_, project, cwd := tiers(t)
	writeSkill(t, project, "report.md", "---\ndescription: 生成周报\n---\n报告正文 $ARGUMENTS\n")
	writeSkill(t, project, "blank.md", "---\ndescription: 空\n---\n")

	state := NewState(Load(cwd))
	return NewTool(state), state
}

func TestTool(t *testing.T) {
	tool, state := toolFixture(t)
	ctx := context.Background()

	tests := []struct {
		name  string
		args  map[string]any
		want  []string
		isErr bool
	}{
		{
			name:  "unknown name",
			args:  map[string]any{"name": "nope"},
			want:  []string{`Unknown skill "nope"`, "blank, commit, report, review, test"},
			isErr: true,
		},
		{
			name:  "missing name",
			args:  map[string]any{},
			want:  []string{"missing required string argument"},
			isErr: true,
		},
		{
			name: "activation",
			args: map[string]any{"name": "/report", "arguments": "本周"},
			want: []string{"[aether:skill report] 本周", blockLead, "报告正文 本周"},
		},
		{
			name:  "empty body",
			args:  map[string]any{"name": "blank"},
			want:  []string{`skill "blank" has no body to activate`},
			isErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tool.Execute(ctx, tt.args)
			if result.IsError != tt.isErr {
				t.Errorf("IsError = %v, want %v (%s)", result.IsError, tt.isErr, result.Output)
			}
			for _, want := range tt.want {
				if !strings.Contains(result.Output, want) {
					t.Errorf("output misses %q:\n%s", want, result.Output)
				}
			}
		})
	}

	active := state.Active()
	if len(active) != 1 || active[0] != (Activation{Name: "report", Args: "本周"}) {
		t.Errorf("Active() = %+v, want just the report activation", active)
	}
}

// The tool's own surface: it is a read, so the ladder lets it through, and it
// is not one of the built-ins, so no permission rule ever addresses it.
func TestToolSurface(t *testing.T) {
	tool, _ := toolFixture(t)
	if tool.Name() != "Skill" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if tool.Category() != tools.CategoryRead {
		t.Errorf("Category() = %q, want a read", tool.Category())
	}
	if _, ok := any(tool).(tools.Specified); ok {
		t.Error("the Skill tool must not be one permission rules address")
	}
	if title := tool.Title(map[string]any{"name": "/report"}); title != "Skill report" {
		t.Errorf("Title() = %q", title)
	}
	for _, name := range tools.BuiltinNames() {
		if name == tool.Name() {
			t.Errorf("%s is a built-in tool; Skill must stay outside that list", name)
		}
	}
}
