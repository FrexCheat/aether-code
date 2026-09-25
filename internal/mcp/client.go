package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type serverConn struct {
	cfg     config.MCPServerConfig
	session *mcp.ClientSession
	// stderr keeps the tail a stdio server writes; a dying server's own
	// diagnostics ride along with the next failure instead of being dropped.
	stderr *tailBuffer
}

func (c *serverConn) connect(ctx context.Context) error {
	client := mcp.NewClient(&mcp.Implementation{Name: "aether-code", Version: version.Version}, nil)
	session, err := client.Connect(ctx, c.transport(), nil)
	if err != nil {
		return c.withStderr(err)
	}
	c.session = session
	return nil
}

// withStderr decorates a transport failure with the captured stderr tail.
func (c *serverConn) withStderr(err error) error {
	if c.stderr == nil {
		return err
	}
	if tail := c.stderr.String(); tail != "" {
		return fmt.Errorf("%w\n[stderr] %s", err, tail)
	}
	return err
}

func (c *serverConn) transport() mcp.Transport {
	if c.cfg.Command != "" {
		cmd := exec.Command(c.cfg.Command, c.cfg.Args...)
		cmd.Env = envWithOverrides(c.cfg.Env)
		c.stderr = &tailBuffer{}
		cmd.Stderr = c.stderr
		return &mcp.CommandTransport{Command: cmd}
	}
	return &mcp.StreamableClientTransport{
		Endpoint: c.cfg.URL,
		HTTPClient: &http.Client{
			Transport: &headerTransport{headers: c.cfg.Headers},
		},
		DisableStandaloneSSE: true,
	}
}

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

func (c *serverConn) callTool(ctx context.Context, rawName string, args map[string]any) (string, bool, error) {
	result, err := c.session.CallTool(ctx, &mcp.CallToolParams{Name: rawName, Arguments: args})
	if err != nil {
		return "", true, c.withStderr(err)
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

// stderrTailSize bounds the stderr a stdio server may leave behind.
const stderrTailSize = 4 * 1024

// tailBuffer keeps the most recent bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > stderrTailSize {
		b.buf = b.buf[len(b.buf)-stderrTailSize:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}

// envWithOverrides returns an environment where the user's values win:
// appending them after os.Environ() is not enough, because a child resolves
// a duplicated name to the first entry.
func envWithOverrides(overrides map[string]string) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+len(overrides))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if _, overridden := overrides[key]; !overridden {
			out = append(out, entry)
		}
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}

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
