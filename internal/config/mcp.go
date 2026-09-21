package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"
)

// UserConfigDir is the per-user AetherCode directory under the home directory,
// where the user-level config file lives.
const UserConfigDir = ".aethercode"

// MCPServerConfig declares one MCP server. The transport is implied: Command
// means a stdio subprocess, URL means a remote Streamable HTTP endpoint.
type MCPServerConfig struct {
	Command  string            `yaml:"command"`
	Args     []string          `yaml:"args"`
	Env      map[string]string `yaml:"env"`
	URL      string            `yaml:"url"`
	Headers  map[string]string `yaml:"headers"`
	Disabled bool              `yaml:"disabled"`
}

// loadMCPServers merges the user-level mcp_servers under the project-level
// ones, expands ${VAR} references, and drops disabled and invalid entries.
// The user layer is tolerant: a missing or broken file costs a warning, never
// an error — one bad server must never take the session down.
func (c *Config) loadMCPServers() {
	userServers, warnings := loadUserMCPServers()

	merged := make(map[string]MCPServerConfig, len(userServers)+len(c.MCPServers))
	maps.Copy(merged, userServers)
	maps.Copy(merged, c.MCPServers)

	for _, name := range slices.Sorted(maps.Keys(merged)) {
		srv := merged[name]
		if srv.Disabled {
			delete(merged, name)
			continue
		}
		if err := srv.validate(); err != nil {
			warnings = append(warnings, fmt.Sprintf("mcp server %q: %v", name, err))
			delete(merged, name)
			continue
		}
		srv.Env = expandValues(srv.Env)
		srv.Headers = expandValues(srv.Headers)
		merged[name] = srv
	}

	c.MCPServers = merged
	c.MCPServerWarnings = warnings
}

// loadUserMCPServers reads mcp_servers from the user-level config file.
// Providers and everything else there are ignored: they are project-private.
func loadUserMCPServers() (map[string]MCPServerConfig, []string) {
	empty := map[string]MCPServerConfig{}
	path, ok := userConfigPath()
	if !ok {
		return empty, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return empty, nil
		}
		return empty, []string{fmt.Sprintf("%s: %v", path, err)}
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return empty, []string{fmt.Sprintf("%s: invalid YAML, ignored: %v", path, err)}
	}
	if cfg.MCPServers == nil {
		return empty, nil
	}
	return cfg.MCPServers, nil
}

// userConfigPath is the user-level config file, or false when the home
// directory cannot be resolved.
func userConfigPath() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	return filepath.Join(home, UserConfigDir, FileName), true
}

func (c MCPServerConfig) validate() error {
	hasCommand := c.Command != ""
	hasURL := c.URL != ""
	switch {
	case hasCommand && hasURL:
		return errors.New("command and url are mutually exclusive")
	case !hasCommand && !hasURL:
		return errors.New("one of command or url is required")
	}
	return nil
}

// expandValues expands ${VAR} references in each value. Unset variables
// expand to empty, which is legitimate: some servers accept empty headers.
func expandValues(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	expanded := make(map[string]string, len(values))
	for k, v := range values {
		expanded[k] = os.ExpandEnv(v)
	}
	return expanded
}
