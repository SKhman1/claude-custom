package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"

	"claude-custom/internal/catalog"
	"claude-custom/internal/config"
	"claude-custom/internal/keystore"
)

// Version is the reported build version; override with -ldflags "-X ...".
var Version = "dev"

const usage = `claude-custom - run Claude Code against any OpenAI-compatible model

Usage:
  claude-custom                    Choose a profile and launch Claude Code
  claude-custom run [flags] [-- claude args...]
  claude-custom status             Show proxy and profile status
  claude-custom doctor             Check the local setup

  claude-custom serve [flags]      Run the local proxy in the foreground
  claude-custom stop               Stop a running proxy
  claude-custom logs [-f] [--lines N]  Read the proxy log

  claude-custom setup [flags]      Reconfigure provider/model or script setup
  claude-custom profile list|use <name>|show [name]|remove <name>
  claude-custom provider list
  claude-custom model list [--profile <name>] [--refresh]
  claude-custom key set <profile>|remove <profile>|status <profile>
  claude-custom config path|show
  claude-custom version            Print the version

Run "claude-custom <command> -h" for command flags.
`

func Run(args []string) (int, error) {
	if len(args) == 0 {
		if err := defaultCommand(); err != nil {
			return 1, err
		}
		return 0, nil
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0, nil
	case "version", "--version":
		fmt.Printf("claude-custom %s\n", Version)
		return 0, nil
	case "setup":
		if err := setupCommand(rest); err != nil {
			if errors.Is(err, errCanceled) {
				return 0, nil
			}
			return 0, err
		}
		return 0, nil
	case "run":
		return runCommand(rest)
	case "serve":
		return 0, serveCommand(rest)
	case "stop":
		return 0, stopCommand()
	case "status":
		return 0, statusCommand()
	case "doctor":
		return 0, doctorCommand()
	case "logs":
		return 0, logsCommand(rest)
	case "profile":
		return 0, profileCommand(rest)
	case "provider":
		return 0, providerCommand(rest)
	case "model":
		return 0, modelCommand(rest)
	case "key":
		return 0, keyCommand(rest)
	case "config":
		return 0, configCommand(rest)
	default:
		return 1, fmt.Errorf("unknown command %q; run `claude-custom help`", cmd)
	}
}

func defaultCommand() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.ApplyEnv()
	if cfg.DefaultProfile == "" {
		if err := setupCommand(nil); err != nil {
			if errors.Is(err, errCanceled) {
				return nil
			}
			return err
		}
		return launchClaude()
	}

	name, configure, err := chooseLaunchProfile(cfg)
	if err != nil {
		if errors.Is(err, errCanceled) {
			return nil
		}
		return err
	}
	if configure {
		if err := setupCommand(nil); err != nil {
			if errors.Is(err, errCanceled) {
				return nil
			}
			return err
		}
		return launchClaude()
	}
	if name != cfg.DefaultProfile {
		cfg.DefaultProfile = name
		if err := config.Save(cfg); err != nil {
			return err
		}
	}
	if err := rechooseModel(cfg, name); err != nil {
		if errors.Is(err, errCanceled) {
			return nil
		}
		return err
	}
	return launchClaude()
}

func launchClaude() error {
	fmt.Println()
	fmt.Println(cyan("❯") + " " + dimmed("Launching Claude Code..."))
	_, err := runCommand(nil)
	return err
}

func loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	cfg.ApplyEnv()
	return cfg, nil
}

func newToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func providerKeyFor(cfg *config.Config, name string) (string, error) {
	profile, ok := cfg.Profiles[name]
	if !ok {
		return "", fmt.Errorf("profile %q not found", name)
	}
	return profile.Provider, nil
}

func lookupKey(cfg *config.Config, profileName string) (string, string, error) {
	profile, provider, err := cfg.Resolve(profileName)
	if err != nil {
		return "", "", err
	}
	if provider.NoAuth {
		return profile.Provider, "", nil
	}
	key, err := keystore.Get(profile.Provider)
	if err != nil && !errors.Is(err, keystore.ErrNotFound) {
		return "", "", err
	}
	if key == "" && !isLocalURL(profile.BaseURL) {
		return "", "", fmt.Errorf("no API key stored for profile %q; run `claude-custom key set %s`", profileName, profileName)
	}
	return profile.Provider, key, nil
}

func isLocalURL(base string) bool {
	for _, prefix := range []string{"http://127.0.0.1", "http://localhost", "http://[::1]"} {
		if len(base) >= len(prefix) && base[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func presetFor(provider config.Provider) (catalog.Preset, bool) {
	if provider.Preset != "" {
		return catalog.Find(provider.Preset)
	}
	return catalog.Preset{}, false
}

// parseFlags parses a command's flags with a clean usage line. It reports
// whether the command should continue; -h/--help returns ok=false without
// an error so the process exits successfully.
func parseFlags(fs *flag.FlagSet, args []string, synopsis string) (bool, error) {
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: claude-custom %s\n\nFlags:\n", synopsis)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
