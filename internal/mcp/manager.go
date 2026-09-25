package mcp

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const connectTimeout = 30 * time.Second

// shutdownGrace caps how long Shutdown waits for the courtesy session
// termination; a healthy server answers in well under a second.
const shutdownGrace = 1500 * time.Millisecond

const (
	toolPrefix    = tools.MCPToolPrefix
	toolSeparator = "__"
	// maxToolName is the API-side limit on tool names; a longer name would be
	// rejected by the provider and poison every request, not just this tool's
	// calls.
	maxToolName = 64
)

const (
	deferredToolThreshold = 20
	officialAnthropicHost = "api.anthropic.com"
	envLoadingOverride    = "AETHERCODE_MCP_LOADING"
)

type LoadingMode string

const (
	LoadingEager    LoadingMode = "eager"
	LoadingNative   LoadingMode = "native"
	LoadingDispatch LoadingMode = "dispatch"
)

func Sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, s)
}

func Qualified(server, rawTool string) string {
	return toolPrefix + Sanitize(server) + toolSeparator + Sanitize(rawTool)
}

func ParseQualifiedName(name string) (server, tool string, ok bool) {
	rest, ok := strings.CutPrefix(name, toolPrefix)
	if !ok {
		return "", "", false
	}
	head, tail, found := strings.Cut(rest, toolSeparator)
	if !found || head == "" || tail == "" {
		return "", "", false
	}
	return head, tail, true
}

func decideMode(protocol config.Protocol, baseURL string, toolCount int) LoadingMode {
	switch LoadingMode(os.Getenv(envLoadingOverride)) {
	case LoadingEager, LoadingNative, LoadingDispatch:
		return LoadingMode(os.Getenv(envLoadingOverride))
	}
	if toolCount < deferredToolThreshold {
		return LoadingEager
	}
	if protocol == config.ProtocolAnthropic && officialAnthropicEndpoint(baseURL) {
		return LoadingNative
	}
	return LoadingDispatch
}

func officialAnthropicEndpoint(baseURL string) bool {
	u, err := url.Parse(baseURL)
	return err == nil && strings.EqualFold(u.Hostname(), officialAnthropicHost)
}

type ServerStatus struct {
	Name  string
	Tools int
	Err   string
}

type Result struct {
	Mode     LoadingMode
	Servers  []ServerStatus
	Warnings []string
}

type Manager struct {
	mu       sync.Mutex
	cfgs     map[string]config.MCPServerConfig
	warnings []string
	conns    map[string]*serverConn
	wrappers map[string]*Tool
	mode     LoadingMode
	cancel   context.CancelFunc
}

func NewManager(cfgs map[string]config.MCPServerConfig, warnings []string) *Manager {
	return &Manager{
		cfgs:     cfgs,
		warnings: warnings,
		conns:    map[string]*serverConn{},
		wrappers: map[string]*Tool{},
	}
}

// settledServer is one server's outcome before it becomes a status line.
type settledServer struct {
	name  string
	conn  *serverConn
	tools []*mcp.Tool
	err   error
}

// Settle connects every configured server in parallel, then admits the
// results: wrappers built in server-name order, a loading mode, one batch of
// registrations. It runs once; a server that fails costs a warning and zero
// tools, never the session.
func (m *Manager) Settle(ctx context.Context, registry *tools.Registry, protocol config.Protocol, baseURL string) Result {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return Result{Mode: m.mode, Warnings: m.warnings}
	}
	settleCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	cfgs := maps.Clone(m.cfgs)
	m.mu.Unlock()

	names := slices.Sorted(maps.Keys(cfgs))
	results := make([]settledServer, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			connCtx, stop := context.WithTimeout(settleCtx, connectTimeout)
			defer stop()

			conn := &serverConn{cfg: cfgs[name]}
			list, err := connectAndList(connCtx, conn)
			results[i] = settledServer{name: name, conn: conn, tools: list, err: err}
		}(i, name)
	}
	wg.Wait()

	return m.admit(registry, results, protocol, baseURL)
}

func connectAndList(ctx context.Context, conn *serverConn) ([]*mcp.Tool, error) {
	if err := conn.connect(ctx); err != nil {
		return nil, err
	}
	return conn.listTools(ctx)
}

// admit turns the settle results into registrations. Everything here happens
// once: duplicate and oversized names are skipped with a warning, the mode is
// decided on the surviving tool count, and the whole batch lands under one
// registry write so no request sees a half-settled tool list.
func (m *Manager) admit(registry *tools.Registry, results []settledServer, protocol config.Protocol, baseURL string) Result {
	warnings := slices.Clone(m.warnings)
	var statuses []ServerStatus
	live := map[string]*serverConn{}
	var all []*Tool
	taken := map[string]bool{}

	for _, s := range results {
		status := ServerStatus{Name: s.name}
		if s.err != nil {
			status.Err = s.err.Error()
			warnings = append(warnings, fmt.Sprintf("mcp server %q: %v", s.name, s.err))
		} else {
			status.Tools = len(s.tools)
			live[s.name] = s.conn
			for _, def := range s.tools {
				w := newTool(s.name, def, s.conn)
				switch {
				case len(w.qualified) > maxToolName:
					warnings = append(warnings, fmt.Sprintf("mcp server %q: tool %q skipped: name exceeds %d characters",
						s.name, def.Name, maxToolName))
				case taken[w.qualified] || registry.Get(w.qualified) != nil:
					warnings = append(warnings, fmt.Sprintf("mcp server %q: tool %q skipped: name collides with another tool",
						s.name, def.Name))
				default:
					taken[w.qualified] = true
					all = append(all, w)
				}
			}
		}
		statuses = append(statuses, status)
	}

	mode := decideMode(protocol, baseURL, len(all))

	m.mu.Lock()
	switch mode {
	case LoadingEager:
		for _, w := range all {
			registry.Register(w)
			m.wrappers[w.qualified] = w
		}
	case LoadingNative:
		for _, w := range all {
			w.deferred = true
			registry.Register(w)
			m.wrappers[w.qualified] = w
		}
		registry.Register(&ToolSearchTool{mgr: m})
	default: // LoadingDispatch: only the discovery tools reach the registry
		registry.Register(&ToolSearchTool{mgr: m})
		registry.Register(&McpCallTool{mgr: m})
		for _, w := range all {
			m.wrappers[w.qualified] = w
		}
	}
	m.conns = live
	m.mode = mode
	m.mu.Unlock()

	return Result{Mode: mode, Servers: statuses, Warnings: warnings}
}

func (m *Manager) DeferredNames() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.mode {
	case LoadingNative, LoadingDispatch:
		return slices.Sorted(maps.Keys(m.wrappers))
	default:
		return nil
	}
}

// modeIs reports whether the session settled into the given mode.
func (m *Manager) modeIs(mode LoadingMode) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode == mode
}

func (m *Manager) resolveTool(server, tool string) (*Tool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if w, ok := m.wrappers[tool]; ok {
		return w, nil
	}
	if server != "" {
		if w, ok := m.wrappers[Qualified(server, tool)]; ok {
			return w, nil
		}
	}
	name := toolSeparator + Sanitize(tool)
	var matches []*Tool
	for _, qualified := range slices.Sorted(maps.Keys(m.wrappers)) {
		if strings.HasSuffix(qualified, name) {
			matches = append(matches, m.wrappers[qualified])
		}
	}
	available := strings.Join(slices.Sorted(maps.Keys(m.wrappers)), ", ")
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("Mcp: unknown tool %q; available tools: %s", tool, available)
	default:
		return nil, fmt.Errorf("Mcp: %q matches %d tools; name it fully; available tools: %s",
			tool, len(matches), available)
	}
}

// Shutdown ends every session, waiting at most shutdownGrace for the servers
// to acknowledge. The closes run in parallel — each is an independent network
// round trip — and the wait is capped because this runs on the exit path: a
// server that stops answering, or a pooled connection that died silently
// during a long session, must delay the exit by a bound, never indefinitely.
// Whatever is still in flight at the deadline finishes with the process.
func (m *Manager) Shutdown() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	conns := slices.Collect(maps.Values(m.conns))
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for _, conn := range conns {
			wg.Go(func() { _ = conn.session.Close() })
		}
		wg.Wait()
	}()

	timer := time.NewTimer(shutdownGrace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}
