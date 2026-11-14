package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var ErrNotFound = errors.New("claude executable not found on PATH")

func LookPath() (string, error) {
	path, err := exec.LookPath("claude")
	if err != nil {
		return "", ErrNotFound
	}
	return path, nil
}

// Env builds the child environment for Claude Code pointed at the local proxy.
// Inherited ANTHROPIC_* variables are removed so the gateway settings are the
// only source of truth. contextWindow is the active model's context window;
// 0 means unknown (falls back to the 200k-class default).
func Env(base []string, proxyURL, token string, contextWindow int) []string {
	out := make([]string, 0, len(base)+16)
	for _, entry := range base {
		name := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			name = entry[:i]
		}
		if strings.HasPrefix(name, "ANTHROPIC_") {
			continue
		}
		if name == "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC" {
			continue
		}
		out = append(out, entry)
	}
	out = append(out,
		"ANTHROPIC_BASE_URL="+proxyURL,
		"ANTHROPIC_AUTH_TOKEN="+token,
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1",
		"CLAUDE_CODE_AUTO_MODE_SERVER=0",
		"CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1",
		fmt.Sprintf("CLAUDE_CODE_AUTO_COMPACT_WINDOW=%d", autoCompactWindow(contextWindow)),
		"CLAUDE_ENABLE_STREAM_WATCHDOG=0",
		"CLAUDE_ENABLE_BYTE_WATCHDOG=0",
		"API_FORCE_IDLE_TIMEOUT=0",
		"API_TIMEOUT_MS=2147483647",
		"DISABLE_AUTOUPDATER=1",
		"DISABLE_FEEDBACK_COMMAND=1",
		"DISABLE_ERROR_REPORTING=1",
	)
	return out
}

// autoCompactWindow returns the token threshold at which Claude Code should
// auto-compact: ~90% of the model's context window, so small-context models
// compact before requests balloon (large attachments otherwise push requests
// past provider and 32MB client limits). Unknown windows keep the legacy 190k.
func autoCompactWindow(contextWindow int) int {
	const fallback = 190000
	if contextWindow <= 0 {
		return fallback
	}
	limit := contextWindow * 9 / 10
	if limit > fallback {
		return fallback
	}
	if limit < 4096 {
		return 4096
	}
	return limit
}

func Run(ctx context.Context, exe string, args []string, env []string) error {
	if exe == "" {
		return ErrNotFound
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start claude: %w", err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ctx.Err()
	case err := <-done:
		if err == nil {
			return nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return &ExitError{Code: exitErr.ExitCode()}
		}
		return err
	}
}

type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("claude exited with code %d", e.Code)
}
