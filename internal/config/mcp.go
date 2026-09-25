package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type MCPServerConfig struct {
	Command  string            `yaml:"command"`
	Args     []string          `yaml:"args"`
	Env      map[string]string `yaml:"env"`
	URL      string            `yaml:"url"`
	Headers  map[string]string `yaml:"headers"`
	Disabled bool              `yaml:"disabled"`
}

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

func userConfigPath() (string, bool) {
	root := DataRoot()
	if root == "" {
		return "", false
	}
	return filepath.Join(root, userConfig), true
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
// Only the braced form expands — os.ExpandEnv would also eat a bare $VAR,
// silently mangling passwords and header values that contain a dollar sign.
func expandValues(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	expanded := make(map[string]string, len(values))
	for k, v := range values {
		expanded[k] = expandBraced(v)
	}
	return expanded
}

// expandBraced replaces ${VAR} with the variable's value and $$ with a
// literal dollar; every other dollar survives untouched.
func expandBraced(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case s[i] != '$':
			b.WriteByte(s[i])
			i++
		case i+1 < len(s) && s[i+1] == '$':
			b.WriteByte('$')
			i += 2
		case i+1 < len(s) && s[i+1] == '{':
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				b.WriteByte('$')
				i++
				continue
			}
			b.WriteString(os.Getenv(s[i+2 : i+2+end]))
			i += 2 + end + 1
		default:
			b.WriteByte('$')
			i++
		}
	}
	return b.String()
}
