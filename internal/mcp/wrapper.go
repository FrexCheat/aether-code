package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/FrexCheat/aether-code/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callTimeout bounds one tool call: a hung server must not wedge the loop
// forever, but inherits the agent's context, so a cancelled turn still
// cancels it immediately.
const callTimeout = 120 * time.Second

// maxDescriptionChars matches the API-side description budget; a longer
// server-provided text would bloat every request.
const maxDescriptionChars = 2048

// Tool wraps one MCP server tool as an ordinary tools.Tool. Its Category is
// always CategoryMCP: permission asks for every MCP call outside bypass
// mode, and rules anchor on the qualified name.
type Tool struct {
	server      string
	rawName     string
	qualified   string
	description string
	inputSchema map[string]any
	deferred    bool
	conn        *serverConn
}

func newTool(server string, def *mcp.Tool, conn *serverConn) *Tool {
	schema, _ := def.InputSchema.(map[string]any)
	if schema == nil {
		schema = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return &Tool{
		server:      server,
		rawName:     def.Name,
		qualified:   Qualified(server, def.Name),
		description: truncateDescription(def.Description),
		inputSchema: schema,
		conn:        conn,
	}
}

func (t *Tool) Name() string             { return t.qualified }
func (t *Tool) Description() string      { return t.description }
func (t *Tool) Category() tools.Category { return tools.CategoryMCP }
func (t *Tool) Schema() tools.ToolSchema {
	return tools.ToolSchema{
		Name:        t.qualified,
		Description: t.description,
		InputSchema: t.inputSchema,
		Deferred:    t.deferred,
	}
}

// Title names the call in the UI after its origin, not its qualified name.
func (t *Tool) Title(args map[string]any) string { return t.server + " · " + t.rawName }

func (t *Tool) Execute(ctx context.Context, args map[string]any) tools.ToolResult {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	text, isError, err := t.conn.callTool(callCtx, t.rawName, args)
	if err != nil {
		// A dead server is a failed result, not a failed loop: the model can
		// pick a different tool and the user can keep working.
		return tools.ToolResult{Output: fmt.Sprintf("Mcp: call failed: %v", err), IsError: true}
	}
	return tools.ToolResult{Output: tools.TruncateOutput(text), IsError: isError}
}

func truncateDescription(s string) string {
	runes := []rune(s)
	if len(runes) <= maxDescriptionChars {
		return s
	}
	return string(runes[:maxDescriptionChars]) + "… [truncated]"
}
