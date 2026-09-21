package main

import (
	"fmt"
	"os"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/mcp"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/prompt"
	"github.com/FrexCheat/aether-code/internal/tools"
	"github.com/FrexCheat/aether-code/internal/tui"
	"github.com/FrexCheat/aether-code/internal/version"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		fail(err)
	}
	active := cfg.Active()
	systemPrompt := prompt.BuildSystemPrompt(prompt.DetectEnvironment(cwd))
	client, err := llm.NewClient(active, nil, systemPrompt)
	if err != nil {
		fail(err)
	}

	// One shared read-state cache: Read records, Write/Edit gate on it.
	fileStates := tools.NewFileStateCache()
	registry := tools.NewRegistry()
	registry.Register(&tools.ReadFileTool{WorkDir: cwd, FileStateCache: fileStates})
	registry.Register(&tools.WriteFileTool{WorkDir: cwd, FileStateCache: fileStates})
	registry.Register(&tools.EditFileTool{WorkDir: cwd, FileStateCache: fileStates})
	registry.Register(&tools.BashTool{WorkDir: cwd})
	registry.Register(&tools.GlobTool{WorkDir: cwd})
	registry.Register(&tools.GrepTool{WorkDir: cwd})

	// One MCP manager for the session: it connects the configured servers in
	// the background once the TUI is up, and its wrappers land in this same
	// registry, so agent and UI treat them like any other tool.
	mcpMgr := mcp.NewManager(cfg.MCPServers, cfg.MCPServerWarnings)

	// One checker for the whole session: the agent decides with it and the UI
	// switches its mode, so what the status bar shows is what gets enforced.
	checker, err := permissions.NewChecker(cwd)
	if err != nil {
		fail(err)
	}

	conv := conversation.NewManager()
	ag := agent.New(client, registry, checker, mcpMgr.DeferredNames)

	if err := tui.Run(tui.Options{
		Agent:        ag,
		Conv:         conv,
		Registry:     registry,
		MCP:          mcpMgr,
		Provider:     active,
		ProviderName: active.Name,
		ModelName:    active.Model,
		Version:      version.Version,
		WorkDir:      cwd,
	}); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "aethercode:", err)
	os.Exit(1)
}
