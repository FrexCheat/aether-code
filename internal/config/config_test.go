package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadSample copies testdata/<name>.yaml into a temp dir as aethercode.yaml and loads it.
func loadSample(t *testing.T, name string) (*Config, string, error) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", name+".yaml"))
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), src, 0o600); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	cfg, err := Load(dir)
	return cfg, filepath.Join(dir, FileName), err
}

func TestLoadValid(t *testing.T) {
	cfg, _, err := loadSample(t, "valid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	active := cfg.Active()
	if active.Name != "claude" {
		t.Errorf("Active().Name = %q, want claude", active.Name)
	}
	if !active.Thinking {
		t.Error("Active().Thinking = false, want true")
	}
	if active.Protocol != ProtocolAnthropic {
		t.Errorf("Active().Protocol = %q", active.Protocol)
	}
	if len(cfg.Providers) != 2 || cfg.Providers[1].Thinking {
		t.Errorf("second provider Thinking should default to false: %+v", cfg.Providers)
	}
}

func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), FileName) {
		t.Errorf("error should mention path %s/%s: %v", dir, FileName, err)
	}
}

func TestLoadMissingAPIKey(t *testing.T) {
	_, path, err := loadSample(t, "missing_api_key")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "providers[0].api_key is required") {
		t.Errorf("error should name field: %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should contain path %s: %v", path, err)
	}
}

func TestLoadDuplicateName(t *testing.T) {
	_, _, err := loadSample(t, "duplicate_name")
	if err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Errorf("expected duplicate error, got %v", err)
	}
}

func TestLoadBadDefault(t *testing.T) {
	_, _, err := loadSample(t, "bad_default")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "not found") || !strings.Contains(msg, "available providers") {
		t.Errorf("expected not-found error listing providers, got %v", err)
	}
	if !strings.Contains(msg, "claude") || !strings.Contains(msg, "gpt") {
		t.Errorf("expected provider names in error, got %v", err)
	}
}

func TestLoadBadProtocol(t *testing.T) {
	_, _, err := loadSample(t, "bad_protocol")
	if err == nil || !strings.Contains(err.Error(), "protocol must be") {
		t.Errorf("expected protocol error, got %v", err)
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("default: [oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "invalid YAML") {
		t.Errorf("expected YAML error, got %v", err)
	}
}
