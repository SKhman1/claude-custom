package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	Version = 1
	DirName = ".claude-custom"
)

// Limits controls provider admission.
type Limits struct {
	MaxConcurrency    int `json:"max_concurrency"`
	RequestsPerWindow int `json:"requests_per_window"`
	WindowSeconds     int `json:"window_seconds"`
}

func DefaultLimits() Limits {
	return Limits{MaxConcurrency: 1, RequestsPerWindow: 1, WindowSeconds: 2}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxConcurrency <= 0 {
		l.MaxConcurrency = d.MaxConcurrency
	}
	if l.RequestsPerWindow <= 0 {
		l.RequestsPerWindow = d.RequestsPerWindow
	}
	if l.WindowSeconds <= 0 {
		l.WindowSeconds = d.WindowSeconds
	}
	return l
}

// Profile is a named provider + model bundle.
type Profile struct {
	Provider        string   `json:"provider"`
	Model           string   `json:"model"`
	BaseURL         string   `json:"base_url,omitempty"`
	Protocol        string   `json:"protocol,omitempty"`
	Limits          Limits   `json:"limits"`
	Fallbacks       []string `json:"fallbacks,omitempty"`
	ContextWindow   int      `json:"context_window,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
}

// Provider is a configured endpoint. Secrets live in the OS keyring.
type Provider struct {
	Preset  string            `json:"preset,omitempty"`
	BaseURL string            `json:"base_url"`
	KeyRef  string            `json:"key_ref,omitempty"`
	NoAuth  bool              `json:"no_auth,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type Server struct {
	Bind string `json:"bind"`
	Port int    `json:"port"`
}

type Config struct {
	Version        int                 `json:"version"`
	DefaultProfile string              `json:"default_profile"`
	Profiles       map[string]Profile  `json:"profiles"`
	Providers      map[string]Provider `json:"providers"`
	Server         Server              `json:"server"`
}

func Default() *Config {
	return &Config{
		Version:   Version,
		Profiles:  map[string]Profile{},
		Providers: map[string]Provider{},
		Server:    Server{Bind: "127.0.0.1", Port: 8787},
	}
}

func Dir() (string, error) {
	if v := strings.TrimSpace(os.Getenv("CLAUDE_CUSTOM_HOME")); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, DirName), nil
}

func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func SecretsPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "secrets.json"), nil
}

func RunPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "run.json"), nil
}

func LogPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs", "proxy.log"), nil
}

func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Version == 0 {
		cfg.Version = Version
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]Profile{}
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]Provider{}
	}
	if cfg.Server.Bind == "" {
		cfg.Server.Bind = "127.0.0.1"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8787
	}
	return &cfg, nil
}

func Save(cfg *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tmp, path)
}

// Resolve returns the effective profile and provider, applying defaults.
func (c *Config) Resolve(name string) (Profile, Provider, error) {
	if name == "" {
		name = c.DefaultProfile
	}
	if name == "" {
		return Profile{}, Provider{}, errors.New("no profile configured; run `claude-custom setup`")
	}
	profile, ok := c.Profiles[name]
	if !ok {
		return Profile{}, Provider{}, fmt.Errorf("profile %q not found", name)
	}
	provider, ok := c.Providers[profile.Provider]
	if !ok {
		return Profile{}, Provider{}, fmt.Errorf("provider %q for profile %q not found", profile.Provider, name)
	}
	profile.Limits = profile.Limits.withDefaults()
	if profile.BaseURL == "" {
		profile.BaseURL = provider.BaseURL
	}
	if profile.BaseURL == "" {
		return Profile{}, Provider{}, fmt.Errorf("profile %q has no base URL", name)
	}
	if profile.Model == "" {
		return Profile{}, Provider{}, fmt.Errorf("profile %q has no model", name)
	}
	return profile, provider, nil
}

func (c *Config) ApplyEnv() {
	if v := strings.TrimSpace(os.Getenv("CLAUDE_CUSTOM_PORT")); v != "" {
		var port int
		if _, err := fmt.Sscanf(v, "%d", &port); err == nil && port > 0 && port < 65536 {
			c.Server.Port = port
		}
	}
	if v := strings.TrimSpace(os.Getenv("CLAUDE_CUSTOM_PROFILE")); v != "" {
		c.DefaultProfile = v
	}
}
