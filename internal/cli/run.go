package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"claude-custom/internal/catalog"
	"claude-custom/internal/claude"
	"claude-custom/internal/config"
	"claude-custom/internal/keystore"
	"claude-custom/internal/proxy"
	"claude-custom/internal/state"
)

func runCommand(args []string) (int, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	profileFlag := fs.String("profile", "", "profile name")
	ask := fs.Bool("ask", false, "choose a profile before running")
	if ok, err := parseFlags(fs, args, "run [--profile <name>] [--ask] [-- claude args...]"); err != nil || !ok {
		if err != nil {
			return 1, err
		}
		return 0, nil
	}
	claudeArgs := fs.Args()

	exe, err := claude.LookPath()
	if err != nil {
		return 1, fmt.Errorf("claude executable not found on PATH; install Claude Code with `npm install -g @anthropic-ai/claude-code` or add it to PATH")
	}

	cfg, err := loadConfig()
	if err != nil {
		return 1, err
	}
	if *ask || cfg.DefaultProfile == "" {
		name, err := pickProfile(cfg, cfg.DefaultProfile)
		if err != nil {
			return 1, err
		}
		if _, ok := cfg.Profiles[name]; !ok {
			reloaded, loadErr := loadConfig()
			if loadErr != nil {
				return 1, loadErr
			}
			cfg = reloaded
		}
		cfg.DefaultProfile = name
	}
	profileName := cfg.DefaultProfile
	if *profileFlag != "" {
		profileName = *profileFlag
	}

	token, err := newToken()
	if err != nil {
		return 1, err
	}
	srv, addr, closeLog, err := startProxy(cfg, profileName, token)
	if err != nil {
		return 1, err
	}
	defer closeLog()
	profile, provider, _ := cfg.Resolve(profileName)
	info := state.RunInfo{
		PID:       os.Getpid(),
		Addr:      addr,
		Token:     token,
		Profile:   profileName,
		Provider:  profile.Provider,
		Model:     profile.Model,
		StartedAt: time.Now(),
	}
	if err := state.Write(info); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write run state: %v\n", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		_ = state.Remove()
	}()

	fmt.Fprintf(os.Stderr, "claude-custom: %s -> %s (proxy %s)\n", profile.Provider, provider.BaseURL, addr)
	env := claude.Env(os.Environ(), "http://"+addr, token, profile.ContextWindow)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = claude.Run(ctx, exe, claudeArgs, env)
	var exitErr *claude.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code, nil
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return 1, err
	}
	return 0, nil
}

func serveCommand(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	profileFlag := fs.String("profile", "", "profile name")
	port := fs.Int("port", 0, "override listen port")
	tokenFlag := fs.String("token", "", "override the generated proxy token")
	if ok, err := parseFlags(fs, args, "serve [--profile <name>] [--port N] [--token T]"); err != nil || !ok {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	profileName := cfg.DefaultProfile
	if *profileFlag != "" {
		profileName = *profileFlag
	}
	if profileName == "" {
		return errors.New("no profile configured; run `claude-custom setup`")
	}
	if *port > 0 {
		cfg.Server.Port = *port
	}
	token := *tokenFlag
	if token == "" {
		token, err = newToken()
		if err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, addr, closeLog, err := startProxyWithStop(cfg, profileName, token, stop)
	if err != nil {
		return err
	}
	defer closeLog()
	profile, provider, _ := cfg.Resolve(profileName)
	info := state.RunInfo{
		PID:       os.Getpid(),
		Addr:      addr,
		Token:     token,
		Profile:   profileName,
		Provider:  profile.Provider,
		Model:     profile.Model,
		StartedAt: time.Now(),
	}
	if err := state.Write(info); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write run state: %v\n", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		_ = state.Remove()
	}()

	fmt.Printf("Listening on http://%s\n", addr)
	fmt.Printf("Profile: %s (%s)\n", profile.Provider, provider.BaseURL)
	fmt.Printf("Model:   %s\n", profile.Model)
	fmt.Printf("Token:   %s\n", token)
	fmt.Println("Press Ctrl+C to stop.")

	<-ctx.Done()
	return nil
}

func startProxy(cfg *config.Config, profileName, token string) (*proxy.Server, string, func(), error) {
	return startProxyWithStop(cfg, profileName, token, nil)
}

func startProxyWithStop(cfg *config.Config, profileName, token string, onStop func()) (*proxy.Server, string, func(), error) {
	key, err := lookupKeyFor(cfg, profileName)
	if err != nil {
		return nil, "", func() {}, err
	}
	profile, _, err := cfg.Resolve(profileName)
	if err != nil {
		return nil, "", func() {}, err
	}
	fallbacks, err := resolveFallbacks(cfg, profileName, profile)
	if err != nil {
		return nil, "", func() {}, err
	}
	logger, closeLog := proxyLogger()
	srv, err := proxy.New(cfg, proxy.Options{
		ProfileName: profileName,
		APIKey:      key,
		Token:       token,
		Fallbacks:   fallbacks,
		Logger:      logger,
		OnStop:      onStop,
	})
	if err != nil {
		closeLog()
		return nil, "", func() {}, err
	}
	addr, err := srv.Start()
	if err != nil {
		if cfg.Server.Port == 0 {
			closeLog()
			return nil, "", func() {}, err
		}
		fmt.Fprintf(os.Stderr, "claude-custom: port %d unavailable, using a free port\n", cfg.Server.Port)
		addr, err = srv.StartPort(0)
		if err != nil {
			closeLog()
			return nil, "", func() {}, err
		}
	}
	logger.Printf("serving %s on http://%s", profileName, addr)
	return srv, addr, closeLog, nil
}

func proxyLogger() (*log.Logger, func()) {
	path, err := config.LogPath()
	if err != nil {
		return log.New(os.Stderr, "claude-custom ", log.LstdFlags), func() {}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "claude-custom: cannot create log directory: %v\n", err)
		return log.New(os.Stderr, "claude-custom ", log.LstdFlags), func() {}
	}
	rotateLog(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claude-custom: cannot open log file: %v\n", err)
		return log.New(os.Stderr, "claude-custom ", log.LstdFlags), func() {}
	}
	return log.New(f, "claude-custom ", log.LstdFlags), func() { _ = f.Close() }
}

func rotateLog(path string) {
	const maxSize = 10 << 20
	info, err := os.Stat(path)
	if err != nil || info.Size() < maxSize {
		return
	}
	for i := 4; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
	}
	_ = os.Rename(path, path+".1")
}

func lookupKeyFor(cfg *config.Config, profileName string) (string, error) {
	_, key, err := lookupKey(cfg, profileName)
	if err != nil {
		return "", err
	}
	return key, nil
}

func resolveFallbacks(cfg *config.Config, profileName string, profile config.Profile) ([]proxy.Target, error) {
	var targets []proxy.Target
	seen := map[string]bool{}
	add := func(target proxy.Target) {
		key := target.Name + "|" + target.BaseURL + "|" + target.Model
		if seen[key] || len(targets) >= maxFallbackTargets {
			return
		}
		seen[key] = true
		targets = append(targets, target)
	}
	for _, ref := range profile.Fallbacks {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		name, model, ok := strings.Cut(ref, "/")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(model) == "" {
			return nil, fmt.Errorf("invalid fallback %q; expected provider/model", ref)
		}
		name = strings.TrimSpace(name)
		model = strings.TrimSpace(model)
		fallbackProfile, fallbackProvider, err := cfg.Resolve(name)
		if err != nil {
			return nil, fmt.Errorf("fallback %q: %w", ref, err)
		}
		key, err := keystore.Get(fallbackProfile.Provider)
		if err != nil && !fallbackProvider.NoAuth && !isLocalURL(fallbackProfile.BaseURL) {
			return nil, fmt.Errorf("fallback %q: no API key: %w", ref, err)
		}
		add(proxy.Target{
			Name:    name,
			BaseURL: fallbackProfile.BaseURL,
			APIKey:  key,
			Model:   model,
			Headers: catalog.AttributionHeaders(fallbackProfile.BaseURL, fallbackProvider.Headers),
		})
	}
	for _, target := range automaticFallbacks(cfg, profileName, profile) {
		add(target)
	}
	return targets, nil
}

// maxFallbackTargets bounds one request's failover chain so a broken setup
// fails in seconds instead of walking every provider.
const maxFallbackTargets = 6

// automaticFallbacks builds a failover chain from every other configured
// profile that has a usable key, so a request is not limited to the providers
// the user happened to wire up by hand. Other providers are preferred over
// more models of the same provider, since a provider-wide outage is the case
// worth surviving.
func automaticFallbacks(cfg *config.Config, profileName string, profile config.Profile) []proxy.Target {
	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		if name != profileName {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var out []proxy.Target
	for _, wantDifferentProvider := range []bool{true, false} {
		for _, name := range names {
			candidate := cfg.Profiles[name]
			differentProvider := candidate.Provider != profile.Provider
			if differentProvider != wantDifferentProvider {
				continue
			}
			if candidate.BaseURL == profile.BaseURL && candidate.Model == profile.Model {
				continue
			}
			target, ok := fallbackTarget(cfg, name)
			if !ok {
				continue
			}
			out = append(out, target)
		}
	}
	return out
}

func fallbackTarget(cfg *config.Config, name string) (proxy.Target, bool) {
	candidateProfile, candidateProvider, err := cfg.Resolve(name)
	if err != nil {
		return proxy.Target{}, false
	}
	key, err := keystore.Get(candidateProfile.Provider)
	if err != nil && !candidateProvider.NoAuth && !isLocalURL(candidateProfile.BaseURL) {
		return proxy.Target{}, false
	}
	return proxy.Target{
		Name:      name,
		BaseURL:   candidateProfile.BaseURL,
		APIKey:    key,
		Model:     candidateProfile.Model,
		MaxOutput: candidateProfile.MaxOutputTokens,
		Headers:   catalog.AttributionHeaders(candidateProfile.BaseURL, candidateProvider.Headers),
	}, true
}

const configureProfileValue = "__configure__"

func chooseLaunchProfile(cfg *config.Config) (name string, configure bool, err error) {
	names := make([]string, 0, len(cfg.Profiles))
	for profileName := range cfg.Profiles {
		names = append(names, profileName)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", true, nil
	}
	items := make([]menuItem, 0, len(names)+1)
	defaultIndex := 0
	for _, profileName := range names {
		profile := cfg.Profiles[profileName]
		detail := strings.TrimSpace(profile.Provider + " / " + profile.Model)
		if profileName == cfg.DefaultProfile {
			detail = "default  " + detail
			defaultIndex = len(items)
		}
		items = append(items, menuItem{Label: profileName, Detail: detail, Value: profileName})
	}
	items = append(items, menuItem{
		Label:  "Configure another provider",
		Detail: "add or change provider, model, or API key",
		Value:  configureProfileValue,
	})
	index, err := selectMenu("Launch Claude Code", items, menuOptions{
		Search:   len(items) > 10,
		PageSize: 10,
		Default:  defaultIndex,
	})
	if err != nil {
		return "", false, err
	}
	if items[index].Value == configureProfileValue {
		return "", true, nil
	}
	return items[index].Value, false, nil
}

func pickProfile(cfg *config.Config, current string) (string, error) {
	if len(cfg.Profiles) == 0 {
		if err := setupCommand(nil); err != nil {
			return "", err
		}
		reloaded, err := loadConfig()
		if err != nil {
			return "", err
		}
		if reloaded.DefaultProfile == "" {
			return "", errors.New("setup did not create a profile")
		}
		return reloaded.DefaultProfile, nil
	}
	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]menuItem, len(names))
	for i, name := range names {
		detail := ""
		if name == current {
			detail = "default"
		}
		items[i] = menuItem{Label: name, Detail: detail, Value: name}
	}
	index, err := selectMenu("Choose a profile", items, menuOptions{Search: len(items) > 8, PageSize: 10})
	if err != nil {
		return "", err
	}
	name := names[index]
	if err := rechooseModel(cfg, name); err != nil {
		return "", err
	}
	return name, nil
}

// rechooseModel offers the provider's live model list so an already-saved
// profile can switch models at launch; Enter keeps the current one.
func rechooseModel(cfg *config.Config, name string) error {
	profile, provider, err := cfg.Resolve(name)
	if err != nil {
		return err
	}
	current := profile.Model
	fmt.Println(dimmed("Fetching model list..."))
	var models []catalog.Model
	key, keyErr := lookupKeyFor(cfg, name)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if keyErr != nil {
		fmt.Printf("%s %v\n", dimmed("Could not load API key:"), keyErr)
	} else {
		fetched, fetchErr := catalog.FetchModelsWithHeaders(ctx, profile.BaseURL, key, provider.Headers)
		if fetchErr != nil {
			fmt.Printf("%s %v\n", dimmed("Could not fetch models:"), fetchErr)
		} else {
			models = catalog.FilterChat(fetched)
		}
	}
	model, err := promptModelWithDefault(models, current)
	if err != nil {
		return err
	}
	model = strings.TrimSpace(model)
	if model == "" || model == current {
		return nil
	}
	updated := profile
	updated.Model = model
	for _, m := range models {
		if m.ID == model {
			updated.ContextWindow = m.ContextWindow
			updated.MaxOutputTokens = m.MaxOutput
			break
		}
	}
	cfg.Profiles[name] = updated
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save profile: %w", err)
	}
	fmt.Printf("%s Model updated: %s -> %s\n", dimmed("✓"), current, model)
	return nil
}
