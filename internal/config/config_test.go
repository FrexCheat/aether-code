package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestApplyDefaults(t *testing.T) {
	tests := []struct {
		name       string
		provider   Provider
		wantWindow int
		wantOutput int
	}{
		{"anthropic defaults", Provider{Protocol: ProtocolAnthropic}, 200000, 16384},
		{"openai defaults", Provider{Protocol: ProtocolOpenAI}, 128000, 32000},
		{"explicit values survive", Provider{Protocol: ProtocolAnthropic, ContextWindow: 100000, MaxOutputTokens: 4096}, 100000, 4096},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Default: "p", Providers: []Provider{tt.provider}}
			cfg.applyDefaults()
			got := cfg.Providers[0]
			if got.ContextWindow != tt.wantWindow {
				t.Errorf("ContextWindow = %d, want %d", got.ContextWindow, tt.wantWindow)
			}
			if got.MaxOutputTokens != tt.wantOutput {
				t.Errorf("MaxOutputTokens = %d, want %d", got.MaxOutputTokens, tt.wantOutput)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	valid := func() *Config {
		return &Config{Default: "p", Providers: []Provider{{
			Name:     "p",
			Protocol: ProtocolAnthropic,
			Model:    "m",
			BaseURL:  "https://example.test",
			APIKey:   "k",
		}}}
	}
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid", func(*Config) {}, false},
		{"no providers", func(c *Config) { c.Providers = nil }, true},
		{"missing name", func(c *Config) { c.Providers[0].Name = "" }, true},
		{"unknown protocol", func(c *Config) { c.Providers[0].Protocol = "gemini" }, true},
		{"negative context window", func(c *Config) { c.Providers[0].ContextWindow = -1 }, true},
		{"negative max output", func(c *Config) { c.Providers[0].MaxOutputTokens = -1 }, true},
		{"duplicate names", func(c *Config) { c.Providers = append(c.Providers, c.Providers[0]) }, true},
		{"default not found", func(c *Config) { c.Default = "other" }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid()
			tt.mutate(cfg)
			err := cfg.validate("test.yaml")
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMaxOutputTokensYAMLTag(t *testing.T) {
	data := []byte("default: p\nproviders:\n  - name: p\n    protocol: anthropic\n    model: m\n    base_url: https://example.test\n    api_key: k\n    max_output_tokens: 4096\n")
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := cfg.Providers[0].MaxOutputTokens; got != 4096 {
		t.Errorf("MaxOutputTokens = %d, want 4096 (yaml max_output_tokens)", got)
	}
}
