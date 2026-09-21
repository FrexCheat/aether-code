package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/FrexCheat/aether-code/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const callTimeout = 120 * time.Second

const maxDescriptionChars = 2048

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

func (t *Tool) Title(args map[string]any) string { return t.server + " · " + t.rawName }

func (t *Tool) Execute(ctx context.Context, args map[string]any) tools.ToolResult {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	text, isError, err := t.conn.callTool(callCtx, t.rawName, args)
	if err != nil {
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
