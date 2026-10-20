package skills

import (
	"context"
	"fmt"
	"strings"

	"github.com/FrexCheat/aether-code/internal/tools"
)

// Tool is the Skill tool: the model's way to load a capability package. Its
// result is instructions rather than data, which is why it is built here rather
// than in the tools package.
type Tool struct {
	state *State
}

func NewTool(state *State) *Tool {
	return &Tool{state: state}
}

func (t *Tool) Name() string { return "Skill" }

// CategoryRead is deliberate: an activation runs nothing by itself — it changes
// which instructions the model holds, and the calls those instructions lead to
// walk the permission ladder like any other turn's.
func (t *Tool) Category() tools.Category { return tools.CategoryRead }

func (t *Tool) Description() string { return description }

func (t *Tool) Schema() tools.ToolSchema {
	return tools.ToolSchema{
		Name:        t.Name(),
		Description: description,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Skill name as listed in the available-skills section, without the leading slash",
				},
				"arguments": map[string]any{
					"type":        "string",
					"description": "Optional text substituted for $ARGUMENTS in the skill's instructions",
				},
			},
			"required": []string{"name"},
		},
	}
}

func (t *Tool) Title(args map[string]any) string {
	if name := strings.TrimSpace(stringArg(args, "name")); name != "" {
		return "Skill " + strings.TrimPrefix(name, "/")
	}
	return ""
}

func (t *Tool) Execute(_ context.Context, args map[string]any) tools.ToolResult {
	name := strings.TrimPrefix(strings.TrimSpace(stringArg(args, "name")), "/")
	if name == "" {
		return errorResult(`Skill: missing required string argument "name"`)
	}
	arguments := stringArg(args, "arguments")

	if t.state.Get(name) == nil {
		return errorResult(fmt.Sprintf("Unknown skill %q. Available skills: %s.", name, t.available()))
	}

	block, err := t.state.Deliver(Activation{Name: name, Args: arguments})
	if err != nil {
		return errorResult("Skill: " + err.Error())
	}
	return tools.ToolResult{Output: block}
}

// available names what the model could have asked for instead.
func (t *Tool) available() string {
	skills := t.state.List()
	if len(skills) == 0 {
		return "none"
	}
	names := make([]string, len(skills))
	for i, skill := range skills {
		names[i] = skill.Name
	}
	return strings.Join(names, ", ")
}

// description is model-facing text, so English like the other tool
// descriptions.
const description = `Load a Skill's instructions and follow them for the current request.

The available skills are listed in the available-skills section of the system
prompt, each with a one-line description. Call this with the name as listed —
without the leading slash — when the task matches one of them. The full
instructions come back as this call's result; call this once per task, not once
per step.

Pass what the user asked for in arguments when the instructions refer to a
parameter: it is substituted into them.`

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func errorResult(msg string) tools.ToolResult {
	return tools.ToolResult{Output: msg, IsError: true}
}
