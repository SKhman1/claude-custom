package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"claude-custom/internal/catalog"
	"claude-custom/internal/config"
	"claude-custom/internal/keystore"
)

func setupCommand(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	providerID := fs.String("provider", "", "provider preset ID")
	modelID := fs.String("model", "", "model ID")
	baseURL := fs.String("base-url", "", "override the provider base URL")
	key := fs.String("key", "", "API key (prefer the interactive prompt)")
	profileName := fs.String("profile", "", "profile name")
	fallbacksFlag := fs.String("fallbacks", "", "comma-separated provider/model fallbacks")
	noAuth := fs.Bool("no-auth", false, "provider needs no API key")
	if ok, err := parseFlags(fs, args, "setup [--provider ID] [--model ID] [--base-url URL] [--profile NAME] [--fallbacks A/B,C/D] [--no-auth]"); err != nil || !ok {
		return err
	}
	if *providerID == "" && *modelID == "" && *baseURL == "" && *key == "" && *profileName == "" && *fallbacksFlag == "" {
		printWelcome()
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	var preset catalog.Preset
	if *providerID != "" {
		var ok bool
		preset, ok = catalog.Find(*providerID)
		if !ok {
			return fmt.Errorf("unknown provider %q", *providerID)
		}
	} else {
		preset, err = promptProvider()
		if err != nil {
			return err
		}
	}

	base := strings.TrimSpace(*baseURL)
	if base == "" {
		base = preset.BaseURL
	}
	if base == "" {
		base, err = promptLine("Base URL (OpenAI-compatible, usually ends in /v1)", "")
		if err != nil {
			return err
		}
	}
	if base == "" {
		return fmt.Errorf("base URL is required")
	}
	base = strings.TrimRight(base, "/")

	name := strings.TrimSpace(*profileName)
	if name == "" {
		name = preset.ID
		if preset.ID == "custom" {
			name, err = promptLine("Profile / provider name", "custom")
			if err != nil {
				return err
			}
		}
	}
	if name == "" {
		return fmt.Errorf("profile name is required")
	}

	apiKey := strings.TrimSpace(*key)
	if apiKey == "" && !*noAuth && !isLocalURL(base) {
		stored, storedErr := keystore.Get(name)
		hasStored := storedErr == nil && strings.TrimSpace(stored) != ""
		if hasStored {
			fmt.Printf("%s An API key is already saved for %q.\n", dimmed("i"), name)
			apiKey, err = promptSecret("API key (press Enter to keep the saved one)")
			if err != nil {
				return err
			}
			apiKey = strings.TrimSpace(apiKey)
			if apiKey == "" {
				apiKey = stored
			}
		} else {
			apiKey, err = promptSecret("API key")
			if err != nil {
				return err
			}
			if apiKey == "" {
				return fmt.Errorf("API key is required (or use --no-auth for keyless endpoints)")
			}
		}
	}

	var models []catalog.Model
	if *modelID == "" {
		fmt.Println(dimmed("Fetching model list..."))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		models, err = catalog.FetchModels(ctx, base, apiKey)
		cancel()
		if err != nil {
			fmt.Printf("%s %v\n", dimmed("Could not fetch models:"), err)
			models = preset.Models
		}
		models = catalog.FilterChat(models)
		if len(models) == 0 {
			fmt.Println(dimmed("Enter the model ID exactly as the provider expects it."))
		}
	}

	model := strings.TrimSpace(*modelID)
	if model == "" {
		model, err = promptModel(models)
		if err != nil {
			return err
		}
	}

	limits := preset.Limits
	if limits.MaxConcurrency == 0 && limits.RequestsPerWindow == 0 && limits.WindowSeconds == 0 {
		limits = config.DefaultLimits()
	}

	profile := config.Profile{
		Provider: name,
		Model:    model,
		BaseURL:  base,
		Limits:   limits,
	}
	for _, ref := range strings.Split(*fallbacksFlag, ",") {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if !strings.Contains(ref, "/") {
			return fmt.Errorf("invalid fallback %q; expected provider/model", ref)
		}
		profile.Fallbacks = append(profile.Fallbacks, ref)
	}
	for _, m := range models {
		if m.ID == model {
			profile.ContextWindow = m.ContextWindow
			profile.MaxOutputTokens = m.MaxOutput
			break
		}
	}

	if apiKey != "" {
		if err := keystore.Set(name, apiKey); err != nil {
			return fmt.Errorf("store API key: %w", err)
		}
		if keystore.Source(name) == "file" {
			fmt.Fprintln(os.Stderr, "warning: OS keyring unavailable; the key was stored in the local secrets file fallback")
		}
	}
	cfg.Providers[name] = config.Provider{
		Preset:  preset.ID,
		BaseURL: base,
		NoAuth:  *noAuth,
	}
	cfg.Profiles[name] = profile
	cfg.DefaultProfile = name
	if err := config.Save(cfg); err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("Profile %q saved: %s -> %s\n", name, base, model)
	if len(profile.Fallbacks) > 0 {
		fmt.Printf("Fallbacks: %s\n", strings.Join(profile.Fallbacks, ", "))
	}
	if preset.KeyURL != "" && apiKey == "" && !*noAuth {
		fmt.Printf("Get a key at %s\n", preset.KeyURL)
	}
	fmt.Println(dimmed("Setup complete."))
	return nil
}

func printWelcome() {
	fmt.Println()
	fmt.Println("  " + bold("claude-custom"))
	fmt.Println("  " + dimmed("Use Claude Code with any provider."))
	fmt.Println()
}
