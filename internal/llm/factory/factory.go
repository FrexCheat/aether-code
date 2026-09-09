// Package factory constructs the concrete llm.Provider for a config entry.
// It lives in its own package so the llm root package never imports adapters.
package factory

import (
	"fmt"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/llm/anthropic"
	"github.com/FrexCheat/aether-code/internal/llm/openai"
)

// New returns the adapter matching cfg.Protocol.
func New(cfg config.Provider) (llm.Provider, error) {
	switch cfg.Protocol {
	case config.ProtocolAnthropic:
		return anthropic.New(cfg, nil), nil
	case config.ProtocolOpenAI:
		return openai.New(cfg, nil), nil
	default:
		return nil, fmt.Errorf("unsupported protocol %q", cfg.Protocol)
	}
}
