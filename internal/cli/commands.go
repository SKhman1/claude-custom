package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"claude-custom/internal/catalog"
	"claude-custom/internal/claude"
	"claude-custom/internal/config"
	"claude-custom/internal/keystore"
	"claude-custom/internal/state"
)

func stopCommand() error {
	info, err := state.Read()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("claude-custom is not running.")
			return nil
		}
		return err
	}
	if !state.Alive(info) {
		_ = state.Remove()
		fmt.Println("claude-custom is not running (removed stale state).")
		return nil
	}
	req, err := http.NewRequest(http.MethodPost, healthBase(info.Addr)+"/stop", nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", info.Token)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("stop request failed: %w", err)
	}
	resp.Body.Close()
	_ = state.Remove()
	fmt.Println("Stopped.")
	return nil
}

func statusCommand() error {
	info, err := state.Read()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("claude-custom is not running.")
			return nil
		}
		return err
	}
	running := state.Alive(info)
	fmt.Printf("PID:      %d\n", info.PID)
	fmt.Printf("Address:  http://%s\n", info.Addr)
	fmt.Printf("Profile:  %s\n", info.Profile)
	fmt.Printf("Provider: %s\n", info.Provider)
	fmt.Printf("Model:    %s\n", info.Model)
	if cfg, err := loadConfig(); err == nil {
		if profile, ok := cfg.Profiles[info.Profile]; ok {
			if chain, err := resolveFallbacks(cfg, info.Profile, profile); err == nil {
				for _, target := range chain {
					fmt.Printf("Failover: %s/%s\n", target.Name, target.Model)
				}
			}
		}
	}
	fmt.Printf("Started:  %s\n", info.StartedAt.Format(time.RFC3339))
	if !running {
		fmt.Println("Status:   not running (stale state file)")
		return nil
	}
	fmt.Println("Process:  running")

	req, err := http.NewRequest(http.MethodGet, healthBase(info.Addr)+"/health", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Status:   degraded")
		fmt.Printf("Health:   unreachable (%v)\n", err)
		return nil
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err == nil {
		state := "running"
		if body["status"] == "degraded" {
			state = "degraded"
		}
		fmt.Printf("Status:   %s\n", state)
		fmt.Printf("Health:   %v\n", body["status"])
		if uptime, ok := body["uptime_seconds"].(float64); ok {
			fmt.Printf("Uptime:   %s\n", time.Duration(uptime)*time.Second)
		}
		if counters, ok := body["counters"].(map[string]any); ok {
			fmt.Printf("Requests: %v (streaming %v, retries %v, recoveries %v, fallbacks %v)\n",
				counters["requests"], counters["streaming"], counters["retries"], counters["recoveries"], counters["fallbacks"])
			fmt.Printf("Errors:   rate_limited %v, server %v\n",
				counters["rate_limited"], counters["server_errors"])
		}
		if last, ok := body["last_error"].(string); ok && last != "" {
			fmt.Printf("Last err: %s\n", last)
		}
	}
	return nil
}

func doctorCommand() error {
	fmt.Println("claude-custom doctor")
	cfgPath, _ := config.Path()
	dir, _ := config.Dir()
	fmt.Printf("  config dir: %s\n", dir)
	fmt.Printf("  config:     %s\n", cfgPath)
	if logPath, err := config.LogPath(); err == nil {
		fmt.Printf("  log:        %s\n", logPath)
	}
	if v := os.Getenv("ANTHROPIC_BASE_URL"); v != "" {
		fmt.Printf("  env:        ANTHROPIC_BASE_URL=%s (overridden while running)\n", v)
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		fmt.Println("  env:        ANTHROPIC_API_KEY is set (removed while running)")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.DefaultProfile == "" {
		fmt.Println("  profile:    none (run `claude-custom setup`)")
	} else {
		profile, provider, err := cfg.Resolve(cfg.DefaultProfile)
		if err != nil {
			fmt.Printf("  profile:    %s (error: %v)\n", cfg.DefaultProfile, err)
		} else {
			fmt.Printf("  profile:    %s\n", cfg.DefaultProfile)
			fmt.Printf("  provider:   %s (%s)\n", profile.Provider, provider.BaseURL)
			fmt.Printf("  model:      %s\n", profile.Model)
			fmt.Printf("  key source: %s\n", keystore.Source(profile.Provider))
			if chain, err := resolveFallbacks(cfg, cfg.DefaultProfile, profile); err == nil {
				for _, target := range chain {
					fmt.Printf("  failover:   %s/%s -> %s\n", target.Name, target.Model, target.BaseURL)
				}
			} else {
				fmt.Printf("  failover:   error: %v\n", err)
			}
			if u, err := url.Parse(provider.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
				fmt.Println("  base URL:   invalid (must be http or https)")
			}
		}
	}

	if path, err := claude.LookPath(); err != nil {
		fmt.Println("  claude:     not found on PATH")
		fmt.Println("              install: npm install -g @anthropic-ai/claude-code")
	} else {
		fmt.Printf("  claude:     %s\n", path)
	}

	port := cfg.Server.Port
	if port == 0 {
		port = 8787
	}
	bind := cfg.Server.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, fmt.Sprintf("%d", port)))
	if err != nil {
		fmt.Printf("  port %d:    in use (another proxy may be running)\n", port)
	} else {
		ln.Close()
		fmt.Printf("  port %d:    available\n", port)
	}
	return nil
}

func logsCommand(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	lines := fs.Int("lines", 100, "number of lines to show")
	follow := fs.Bool("f", false, "follow new log entries")
	fs.BoolVar(follow, "follow", false, "follow new log entries")
	if ok, err := parseFlags(fs, args, "logs [-f] [--lines N]"); err != nil || !ok {
		return err
	}
	path, err := config.LogPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Printf("No log file yet at %s. Start the proxy with `claude-custom serve` or `claude-custom run`.\n", path)
			return nil
		}
		return err
	}
	all := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	start := len(all) - *lines
	if start < 0 {
		start = 0
	}
	for _, line := range all[start:] {
		fmt.Println(line)
	}
	if !*follow {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			_, _ = os.Stdout.Write(buf[:n])
		}
		if err == io.EOF {
			time.Sleep(300 * time.Millisecond)
			continue
		}
		if err != nil {
			return err
		}
	}
}

func profileCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: claude-custom profile list|use|show|remove")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if len(cfg.Profiles) == 0 {
			fmt.Println("No profiles. Run `claude-custom setup`.")
			return nil
		}
		names := sortedProfiles(cfg)
		for _, name := range names {
			p := cfg.Profiles[name]
			marker := ""
			if name == cfg.DefaultProfile {
				marker = " *"
			}
			fmt.Printf("%s%s  %s -> %s\n", name, marker, p.Provider, p.Model)
		}
		return nil
	case "use":
		if len(args) < 2 {
			return errors.New("usage: claude-custom profile use <name>")
		}
		if _, ok := cfg.Profiles[args[1]]; !ok {
			return fmt.Errorf("profile %q not found", args[1])
		}
		cfg.DefaultProfile = args[1]
		return config.Save(cfg)
	case "show":
		name := cfg.DefaultProfile
		if len(args) > 1 {
			name = args[1]
		}
		if name == "" {
			return errors.New("no default profile")
		}
		profile, provider, err := cfg.Resolve(name)
		if err != nil {
			return err
		}
		fmt.Printf("profile:  %s\n", name)
		fmt.Printf("provider: %s\n", profile.Provider)
		fmt.Printf("base URL: %s\n", provider.BaseURL)
		fmt.Printf("model:    %s\n", profile.Model)
		fmt.Printf("key:      %s\n", keystore.Source(profile.Provider))
		fmt.Printf("limits:   %d request(s) / %ds, concurrency %d\n",
			profile.Limits.RequestsPerWindow, profile.Limits.WindowSeconds, profile.Limits.MaxConcurrency)
		return nil
	case "remove":
		if len(args) < 2 {
			return errors.New("usage: claude-custom profile remove <name>")
		}
		name := args[1]
		profile, ok := cfg.Profiles[name]
		if !ok {
			return fmt.Errorf("profile %q not found", name)
		}
		delete(cfg.Profiles, name)
		inUse := false
		for _, p := range cfg.Profiles {
			if p.Provider == profile.Provider {
				inUse = true
				break
			}
		}
		if !inUse {
			delete(cfg.Providers, profile.Provider)
		}
		if cfg.DefaultProfile == name {
			cfg.DefaultProfile = ""
		}
		return config.Save(cfg)
	default:
		return fmt.Errorf("unknown profile command %q", args[0])
	}
}

func providerCommand(args []string) error {
	if len(args) == 0 || args[0] != "list" {
		return errors.New("usage: claude-custom provider list")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	configured := map[string]bool{}
	for _, p := range cfg.Providers {
		if p.Preset != "" {
			configured[p.Preset] = true
		}
	}
	for _, preset := range catalog.Presets {
		marker := ""
		if configured[preset.ID] {
			marker = " (configured)"
		}
		fmt.Printf("%-22s %s%s\n", preset.ID, preset.BaseURL, marker)
	}
	return nil
}

func modelCommand(args []string) error {
	if len(args) == 0 || args[0] != "list" {
		return errors.New("usage: claude-custom model list [--profile <name>] [--refresh]")
	}
	fs := flag.NewFlagSet("model list", flag.ContinueOnError)
	profileFlag := fs.String("profile", "", "profile name")
	refresh := fs.Bool("refresh", false, "fetch the live model list from the provider")
	if ok, err := parseFlags(fs, args[1:], "model list [--profile <name>] [--refresh]"); err != nil || !ok {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	name := cfg.DefaultProfile
	if *profileFlag != "" {
		name = *profileFlag
	}
	if name == "" {
		return errors.New("no profile configured")
	}
	profile, provider, err := cfg.Resolve(name)
	if err != nil {
		return err
	}
	if !*refresh {
		if preset, ok := presetFor(provider); ok && len(preset.Models) > 0 {
			for _, m := range preset.Models {
				marker := ""
				if m.ID == profile.Model {
					marker = " (current)"
				}
				fmt.Printf("%-60s %s%s\n", m.ID, m.Name, marker)
			}
		} else if profile.Model != "" {
			fmt.Println(profile.Model)
		}
		fmt.Fprintf(os.Stderr, "%s: showing known presets; use --refresh for the live provider list\n", name)
		return nil
	}
	key, err := lookupKeyFor(cfg, name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	models, err := catalog.FetchModels(ctx, provider.BaseURL, key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not fetch models: %v\n", err)
		if preset, ok := presetFor(provider); ok && len(preset.Models) > 0 {
			fmt.Println("Known models for this preset:")
			for _, m := range preset.Models {
				fmt.Printf("  %s\n", m.ID)
			}
		}
		return nil
	}
	models = catalog.FilterChat(models)
	for _, m := range models {
		marker := ""
		if m.ID == profile.Model {
			marker = " (current)"
		}
		fmt.Printf("%-60s %s%s\n", m.ID, m.Name, marker)
	}
	return nil
}

func keyCommand(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: claude-custom key set|remove|status <profile>")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	name := args[1]
	providerKey, err := providerKeyFor(cfg, name)
	if err != nil {
		return err
	}
	switch args[0] {
	case "set":
		secret, err := promptSecret("API key")
		if err != nil {
			return err
		}
		if secret == "" {
			return errors.New("key is empty")
		}
		if err := keystore.Set(providerKey, secret); err != nil {
			return err
		}
		if keystore.Source(providerKey) == "file" {
			fmt.Fprintln(os.Stderr, "warning: OS keyring unavailable; the key was stored in the local secrets file fallback")
		}
		fmt.Printf("Key stored for %s (%s)\n", name, keystore.Source(providerKey))
		return nil
	case "remove":
		if err := keystore.Delete(providerKey); err != nil {
			return err
		}
		fmt.Printf("Key removed for %s\n", name)
		return nil
	case "status":
		fmt.Printf("%s: %s\n", name, keystore.Source(providerKey))
		return nil
	default:
		return fmt.Errorf("unknown key command %q", args[0])
	}
}

func configCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: claude-custom config path|show")
	}
	switch args[0] {
	case "path":
		path, err := config.Path()
		if err != nil {
			return err
		}
		fmt.Println(path)
		return nil
	case "show":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	default:
		return fmt.Errorf("unknown config command %q", args[0])
	}
}

func sortedProfiles(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func healthBase(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
