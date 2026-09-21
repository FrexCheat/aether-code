package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverConn is one live connection to an MCP server, either a stdio
// subprocess or a Streamable HTTP endpoint.
type serverConn struct {
	cfg          config.MCPServerConfig
	session      *mcp.ClientSession
	instructions string
}

// connect starts the transport the config asks for and runs the initialize
// handshake. The session closes itself if the handshake fails.
func (c *serverConn) connect(ctx context.Context) error {
	client := mcp.NewClient(&mcp.Implementation{Name: "aether-code", Version: version.Version}, nil)
	session, err := client.Connect(ctx, c.transport(), nil)
	if err != nil {
		return err
	}
	c.session = session
	if result := session.InitializeResult(); result != nil {
		c.instructions = result.Instructions
	}
	return nil
}

func (c *serverConn) transport() mcp.Transport {
	if c.cfg.Command != "" {
		cmd := exec.Command(c.cfg.Command, c.cfg.Args...)
		cmd.Env = os.Environ()
		for k, v := range c.cfg.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		// Servers probing for a TTY would push terminal queries into the
		// shared stdin of the TUI; their stderr has nowhere useful to go.
		cmd.Stderr = io.Discard
		return &mcp.CommandTransport{Command: cmd}
	}
	return &mcp.StreamableClientTransport{
		Endpoint: c.cfg.URL,
		HTTPClient: &http.Client{
			Transport: &headerTransport{headers: c.cfg.Headers},
		},
		// The standalone SSE stream only carries server-initiated messages,
		// and nothing outside tools/call is consumed.
		DisableStandaloneSSE: true,
	}
}

// listTools walks every page of the server's tool list.
func (c *serverConn) listTools(ctx context.Context) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	for tool, err := range c.session.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// callTool runs one tool call. A transport or JSON-RPC failure comes back as
// the error; a business failure comes back inside the result, with IsError.
func (c *serverConn) callTool(ctx context.Context, rawName string, args map[string]any) (string, bool, error) {
	result, err := c.session.CallTool(ctx, &mcp.CallToolParams{Name: rawName, Arguments: args})
	if err != nil {
		return "", true, err
	}

	var parts []string
	skipped := 0
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		} else {
			skipped++
		}
	}
	joined := strings.Join(parts, "\n")
	if joined == "" && result.StructuredContent != nil {
		if raw, err := json.MarshalIndent(result.StructuredContent, "", "  "); err == nil {
			joined = string(raw)
		}
	}
	if joined == "" {
		joined = "(no output)"
	}
	if skipped > 0 {
		joined += fmt.Sprintf("\n[%d non-text content block(s) omitted]", skipped)
	}
	return joined, result.IsError, nil
}

// headerTransport injects the configured headers into every request the
// Streamable HTTP transport sends.
type headerTransport struct {
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	for k, v := range t.headers {
		// The MCP protocol headers are the SDK's to manage; a user header
		// under the same name would corrupt the session.
		if strings.HasPrefix(k, "Mcp-") {
			continue
		}
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}
