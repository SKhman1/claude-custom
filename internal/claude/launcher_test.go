package claude

import (
	"os"
	"strings"
	"testing"
)

func TestAutoCompactWindow(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, 190000},       // unknown window keeps legacy default
		{-5, 190000},      // invalid
		{200000, 180000},  // 90% of a 200k window
		{1000000, 190000}, // large window capped
		{131072, 117964},  // 90% of a small window
		{8192, 7372},      // 90% of tiny window
		{4000, 4096},      // clamped to minimum
		{100, 4096},       // absurdly small still clamped
	}
	for _, c := range cases {
		if got := autoCompactWindow(c.in); got != c.want {
			t.Errorf("autoCompactWindow(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestEnvSetsAutoCompactFromContextWindow(t *testing.T) {
	env := Env(os.Environ(), "http://127.0.0.1:8787", "tok", 131072)
	found := false
	for _, entry := range env {
		if entry == "CLAUDE_CODE_AUTO_COMPACT_WINDOW=117964" {
			found = true
		}
		if strings.HasPrefix(entry, "ANTHROPIC_BASE_URL=") && entry != "ANTHROPIC_BASE_URL=http://127.0.0.1:8787" {
			t.Errorf("unexpected base URL entry %q", entry)
		}
	}
	if !found {
		t.Fatalf("context-window-derived auto-compact setting missing from env: %v", env)
	}
}
