package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadToleratesUTF8BOM(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CUSTOM_HOME", dir)
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"version":1,"default_profile":"openrouter","profiles":{"openrouter":{"provider":"openrouter","model":"m"}}}`)...)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultProfile != "openrouter" {
		t.Fatalf("default profile = %q", cfg.DefaultProfile)
	}
	if got := cfg.Profiles["openrouter"].Model; got != "m" {
		t.Fatalf("model = %q", got)
	}
}

func TestLoadDefaultsOnMissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CUSTOM_HOME", dir)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Version != Version {
		t.Fatalf("version = %d", cfg.Version)
	}
}
