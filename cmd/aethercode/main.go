// Command aethercode is the AetherCode command-line AI coding assistant.
//
// This phase accepts no command-line flags: configuration is read from
// aethercode.yaml in the current working directory.
package main

import (
	"fmt"
	"os"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/llm/factory"
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
	provider, err := factory.New(active)
	if err != nil {
		fail(err)
	}
	if err := tui.Run(tui.Options{
		Provider:     provider,
		ProviderName: active.Name,
		ModelName:    active.Model,
		Thinking:     active.Thinking,
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
