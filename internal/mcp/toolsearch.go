package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/FrexCheat/aether-code/internal/tools"
)

// ToolSearchName is the discovery tool present in both deferred modes.
const ToolSearchName = "ToolSearch"

const defaultSearchResults = 5

const toolSearchDescription = `Fetches full definitions for deferred MCP tools so they can be called.

Deferred MCP tools appear by name in <system-reminder> messages. Until fetched, only the name is known — there is no parameter schema, so the tool cannot be invoked. This tool takes a query, matches it against the deferred tool list, and returns the matched tools' complete definitions. Once a definition is returned, the tool is callable like any tool offered at the top of the prompt: by its own name where the provider surfaces the schema, or via McpCall where the definition came back as text.

Query forms:
- "select:Read,Edit" — fetch these exact tools by name
- "notebook jupyter" — keyword search, best matches first
- "+slack send" — require "slack" in the name, rank by the remaining terms`

// ToolSearchTool surfaces the deferred wrappers: natively via tool_reference
// blocks on the result, as text otherwise.
type ToolSearchTool struct {
	mgr *Manager
}

func (t *ToolSearchTool) Name() string        { return ToolSearchName }
func (t *ToolSearchTool) Description() string { return toolSearchDescription }

// CategoryRead: the search is local and harmless, so it never asks.
func (t *ToolSearchTool) Category() tools.Category { return tools.CategoryRead }

func (t *ToolSearchTool) Schema() tools.ToolSchema {
	return tools.ToolSchema{
		Name:        ToolSearchName,
		Description: toolSearchDescription,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "select:names for an exact fetch, keywords for a search, +term to require a name substring",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "maximum number of matches to return (default 5)",
				},
			},
			"required": []string{"query"},
		},
	}
}

// Title names the call in the UI after the query.
func (t *ToolSearchTool) Title(args map[string]any) string {
	query, _ := args["query"].(string)
	return ToolSearchName + " " + query
}

func (t *ToolSearchTool) Execute(ctx context.Context, args map[string]any) tools.ToolResult {
	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return tools.ToolResult{Output: "ToolSearch: query is required", IsError: true}
	}
	maxResults := defaultSearchResults
	if n, ok := args["max_results"].(float64); ok && n >= 1 {
		maxResults = int(n)
	}

	matches := t.mgr.search(query, maxResults)
	if len(matches) == 0 {
		return tools.ToolResult{
			Output:  "ToolSearch: no deferred tool matches " + strconv.Quote(query),
			IsError: true,
		}
	}

	if t.mgr.modeIs(LoadingNative) {
		var names []string
		var lines []string
		for _, w := range matches {
			names = append(names, w.qualified)
			lines = append(lines, "- "+w.qualified+": "+firstLine(w.description))
		}
		return tools.ToolResult{
			Output:   "These tools' schemas are now loaded; call them by name.\n" + strings.Join(lines, "\n"),
			ToolRefs: names,
		}
	}

	var b strings.Builder
	for _, w := range matches {
		schema, _ := json.MarshalIndent(w.inputSchema, "", "  ")
		fmt.Fprintf(&b, "%s — %s\n%s\n\n", w.qualified, firstLine(w.description), schema)
	}
	b.WriteString("Call one of these tools via McpCall with server, tool and arguments.")
	return tools.ToolResult{Output: b.String()}
}

// search matches a query against the wrappers: select: fetches by exact
// name, anything else scores name parts and descriptions, with a
// '+'-prefixed term required inside the name.
func (m *Manager) search(query string, max int) []*Tool {
	if rest, ok := strings.CutPrefix(strings.TrimSpace(query), "select:"); ok {
		var out []*Tool
		for _, name := range strings.Split(rest, ",") {
			if w, err := m.resolveTool("", strings.TrimSpace(name)); err == nil {
				out = append(out, w)
			}
		}
		return out
	}

	var required, terms []string
	for _, field := range strings.Fields(query) {
		if rest, ok := strings.CutPrefix(field, "+"); ok {
			required = append(required, rest)
			continue
		}
		terms = append(terms, field)
	}

	m.mu.Lock()
	all := slices.Collect(maps.Values(m.wrappers))
	m.mu.Unlock()

	type hit struct {
		tool  *Tool
		score int
	}
	var hits []hit
	for _, w := range all {
		matched := true
		for _, r := range required {
			if !strings.Contains(w.qualified, Sanitize(r)) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		score := 0
		if len(terms) == 0 {
			// Required-only queries already filtered the list; rank them all
			// equal instead of dropping every hit.
			score = 1
		}
		for _, term := range terms {
			if strings.Contains(w.qualified, Sanitize(term)) {
				score += 2
			}
			if strings.Contains(strings.ToLower(w.description), strings.ToLower(term)) {
				score++
			}
		}
		if score > 0 {
			hits = append(hits, hit{tool: w, score: score})
		}
	}

	slices.SortStableFunc(hits, func(a, b hit) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return strings.Compare(a.tool.qualified, b.tool.qualified)
	})
	hits = hits[:min(len(hits), max)]

	out := make([]*Tool, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.tool)
	}
	return out
}

func firstLine(s string) string {
	if line, _, _ := strings.Cut(s, "\n"); line != "" {
		return line
	}
	return s
}
