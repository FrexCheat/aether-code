package mcp

import (
	"context"
	"fmt"

	"github.com/FrexCheat/aether-code/internal/tools"
)

// McpCallName is the dispatcher tool present in dispatch mode only.
const McpCallName = "McpCall"

const mcpCallDescription = `Calls an MCP tool that is not in the tool list. Discover deferred MCP tools with ToolSearch first, then run one of them here.

Inputs:
- tool: the MCP tool name, either fully qualified (mcp__server__tool) or the short name as the search result shows it
- server: the server the tool came from; optional when tool is fully qualified or unambiguous
- arguments: an object matching the tool's schema as returned by ToolSearch`

// McpCallTool dispatches to a wrapper chosen from its arguments. It carries
// CategoryMCP, but the agent resolves the target before the permission
// check, so rules and dialogs anchor on the MCP tool's own name.
type McpCallTool struct {
	mgr *Manager
}

func (t *McpCallTool) Name() string        { return McpCallName }
func (t *McpCallTool) Description() string { return mcpCallDescription }
func (t *McpCallTool) Category() tools.Category {
	return tools.CategoryMCP
}

func (t *McpCallTool) Schema() tools.ToolSchema {
	return tools.ToolSchema{
		Name:        McpCallName,
		Description: mcpCallDescription,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tool": map[string]any{
					"type":        "string",
					"description": "the MCP tool name: fully qualified (mcp__server__tool) or the short name from ToolSearch",
				},
				"server": map[string]any{
					"type":        "string",
					"description": "the server the tool came from; optional when tool is fully qualified or unambiguous",
				},
				"arguments": map[string]any{
					"type":        "object",
					"description": "the tool's arguments, matching the schema ToolSearch returned",
				},
			},
			"required": []string{"tool"},
		},
	}
}

// Title names the call in the UI after the tool it dispatches to.
func (t *McpCallTool) Title(args map[string]any) string {
	tool, _ := args["tool"].(string)
	server, _ := args["server"].(string)
	if s, tail, ok := ParseQualifiedName(tool); ok {
		return s + " · " + tail
	}
	if server != "" {
		return server + " · " + tool
	}
	return tool
}

// Target implements tools.Dispatcher.
func (t *McpCallTool) Target(args map[string]any) (tools.Tool, map[string]any, error) {
	w, callArgs, err := t.resolve(args)
	if err != nil {
		return nil, nil, err
	}
	return w, callArgs, nil
}

// Execute satisfies tools.Tool. The agent loop resolves the target through
// Target before it executes, so in the ordinary flow this is not reached.
func (t *McpCallTool) Execute(ctx context.Context, args map[string]any) tools.ToolResult {
	w, callArgs, err := t.resolve(args)
	if err != nil {
		return tools.ToolResult{Output: err.Error(), IsError: true}
	}
	return w.Execute(ctx, callArgs)
}

func (t *McpCallTool) resolve(args map[string]any) (*Tool, map[string]any, error) {
	tool, _ := args["tool"].(string)
	server, _ := args["server"].(string)
	if tool == "" {
		return nil, nil, fmt.Errorf("Mcp: tool is required")
	}
	w, err := t.mgr.resolveTool(server, tool)
	if err != nil {
		return nil, nil, err
	}
	callArgs, _ := args["arguments"].(map[string]any)
	if callArgs == nil {
		callArgs = map[string]any{}
	}
	return w, callArgs, nil
}
