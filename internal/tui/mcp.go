package tui

import (
	"context"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/FrexCheat/aether-code/internal/mcp"
)

// mcpReadyMsg carries the outcome of the background settle.
type mcpReadyMsg struct {
	result mcp.Result
}

// connectMCP settles the configured servers while the TUI is already live.
func (m Model) connectMCP() tea.Cmd {
	if m.opts.MCP == nil {
		return nil
	}
	mgr, registry := m.opts.MCP, m.opts.Registry
	provider := m.opts.Provider
	return func() tea.Msg {
		return mcpReadyMsg{mgr.Settle(context.Background(), registry, provider.Protocol, provider.BaseURL)}
	}
}

// shutdownMCP closes the MCP connections. It runs inside tea.Sequence before
// tea.Quit, so the stdio children are gone when the terminal returns to the
// shell.
func (m Model) shutdownMCP() tea.Cmd {
	mgr := m.opts.MCP
	return func() tea.Msg {
		mgr.Shutdown()
		return nil
	}
}

// mcpSummary renders the settle outcome for the scrollback, empty when no
// server was configured.
func mcpSummary(r mcp.Result) string {
	var total int
	for _, s := range r.Servers {
		total += s.Tools
	}
	if len(r.Servers) == 0 && len(r.Warnings) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("MCP: " + strconv.Itoa(len(r.Servers)) + " server(s), " +
		strconv.Itoa(total) + " tool(s), loading mode " + string(r.Mode) + "\n")
	for _, s := range r.Servers {
		if s.Err != "" {
			b.WriteString("  ✗ " + s.Name + ": " + s.Err + "\n")
			continue
		}
		b.WriteString("  ✓ " + s.Name + ": " + strconv.Itoa(s.Tools) + " tool(s)\n")
	}
	for _, w := range r.Warnings {
		b.WriteString("  warning: " + w + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}
