// Command aethercode is the AetherCode command-line AI coding assistant.
//
// This phase accepts no command-line flags: configuration is read from
// aethercode.yaml in the current working directory.
package main

import (
	"fmt"
	"os"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
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
	client, err := llm.NewClient(active, nil)
	if err != nil {
		fail(err)
	}

	// One shared read-state cache: read_file records, write/edit gate on it.
	fsc := tools.NewFileStateCache()
	registry := tools.NewRegistry()
	registry.Register(&tools.ReadFileTool{WorkDir: cwd, FileStateCache: fsc})
	registry.Register(&tools.WriteFileTool{WorkDir: cwd, FileStateCache: fsc})
	registry.Register(&tools.EditFileTool{WorkDir: cwd, FileStateCache: fsc})
	registry.Register(&tools.BashTool{WorkDir: cwd})
	registry.Register(&tools.GlobTool{WorkDir: cwd})
	registry.Register(&tools.GrepTool{WorkDir: cwd})

	conv := conversation.NewManager()
	orchestrator := agent.New(client, registry, conv, prompt.System, active.Thinking, cfg.Agent.MaxIterations)

	if err := tui.Run(tui.Options{
		Agent:        orchestrator,
		Conv:         conv,
		Registry:     registry,
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
