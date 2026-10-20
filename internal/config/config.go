package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Protocol string

const (
	ProtocolAnthropic Protocol = "anthropic"
	ProtocolOpenAI    Protocol = "openai"
)

// ProjectDir is the per-project runtime directory (settings and permission
// rules); its user-level counterpart is DataRoot.
const (
	ProjectDir    = ".aether"
	projectConfig = "settings.local.yaml"
	userConfig    = "settings.yaml"
)

type Provider struct {
	Name     string   `yaml:"name"`
	Protocol Protocol `yaml:"protocol"`
	Model    string   `yaml:"model"`
	BaseURL  string   `yaml:"base_url"`
	APIKey   string   `yaml:"api_key"`
	Thinking bool     `yaml:"thinking"`
	// Zero means "not configured" and resolves to the protocol default.
	ContextWindow int `yaml:"context_window"`
	// MaxOutputTokens is the per-request output cap. The compaction layer
	// derives its output reserve from it, so the trigger line and the request
	// ceiling never drift apart. Zero resolves to the protocol default.
	MaxOutputTokens int `yaml:"max_output_tokens"`
}

type Config struct {
	Default           string                     `yaml:"default"`
	Providers         []Provider                 `yaml:"providers"`
	MCPServers        map[string]MCPServerConfig `yaml:"mcp_servers"`
	MCPServerWarnings []string                   `yaml:"-"`
}

func Load(dir string) (*Config, error) {
	path := filepath.Join(dir, ProjectDir, projectConfig)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s: invalid YAML: %w", path, err)
	}
	if err := cfg.validate(path); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	cfg.loadMCPServers()
	return &cfg, nil
}

// DataRoot is the user-level aether directory, ~/.aether. User settings,
// permission rules and sessions all live under it. Empty when no home
// directory can be resolved.
func DataRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".aether")
}

func (c *Config) applyDefaults() {
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.ContextWindow == 0 {
			if p.Protocol == ProtocolOpenAI {
				p.ContextWindow = 128000
			} else {
				p.ContextWindow = 200000
			}
		}
		if p.MaxOutputTokens == 0 {
			if p.Protocol == ProtocolOpenAI {
				p.MaxOutputTokens = 32000
			} else {
				p.MaxOutputTokens = 16384
			}
		}
	}
}

func (c *Config) Active() Provider {
	for _, p := range c.Providers {
		if p.Name == c.Default {
			return p
		}
	}
	return Provider{}
}

func (c *Config) validate(path string) error {
	if len(c.Providers) == 0 {
		return fmt.Errorf("%s: providers must contain at least one entry", path)
	}
	seen := make(map[string]int, len(c.Providers))
	names := make([]string, 0, len(c.Providers))
	for i, p := range c.Providers {
		if p.Name == "" {
			return fmt.Errorf("%s: providers[%d].name is required", path, i)
		}
		if p.Protocol == "" {
			return fmt.Errorf("%s: providers[%d].protocol is required", path, i)
		}
		if p.Protocol != ProtocolAnthropic && p.Protocol != ProtocolOpenAI {
			return fmt.Errorf("%s: providers[%d].protocol must be %q or %q, got %q",
				path, i, ProtocolAnthropic, ProtocolOpenAI, p.Protocol)
		}
		if p.Model == "" {
			return fmt.Errorf("%s: providers[%d].model is required", path, i)
		}
		if p.BaseURL == "" {
			return fmt.Errorf("%s: providers[%d].base_url is required", path, i)
		}
		if p.APIKey == "" {
			return fmt.Errorf("%s: providers[%d].api_key is required", path, i)
		}
		if p.ContextWindow < 0 {
			return fmt.Errorf("%s: providers[%d].context_window must not be negative", path, i)
		}
		if p.MaxOutputTokens < 0 {
			return fmt.Errorf("%s: providers[%d].max_output_tokens must not be negative", path, i)
		}
		if j, dup := seen[p.Name]; dup {
			return fmt.Errorf("%s: providers[%d].name %q duplicates providers[%d]", path, i, p.Name, j)
		}
		seen[p.Name] = i
		names = append(names, p.Name)
	}
	if c.Default == "" {
		return fmt.Errorf("%s: default is required", path)
	}
	if _, ok := seen[c.Default]; !ok {
		return fmt.Errorf("%s: default %q not found; available providers: %s",
			path, c.Default, strings.Join(names, ", "))
	}
	return nil
}
