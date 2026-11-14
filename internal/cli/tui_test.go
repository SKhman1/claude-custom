package cli

import (
	"strings"
	"testing"
)

func TestFilterMenuItems(t *testing.T) {
	items := []menuItem{
		{Label: "NVIDIA NIM", Detail: "integrate.api.nvidia.com", Value: "nvidia_nim"},
		{Label: "Ollama (local)", Detail: "localhost:11434", Value: "ollama"},
		{Label: "OpenRouter", Detail: "openrouter.ai", Value: "openrouter"},
	}
	got := filterMenuItems(items, "ollama")
	if len(got) != 1 || got[0].index != 1 {
		t.Fatalf("filter ollama = %+v", got)
	}
	got = filterMenuItems(items, "API")
	if len(got) != 1 || got[0].index != 0 {
		t.Fatalf("filter detail = %+v", got)
	}
	got = filterMenuItems(items, "open")
	if len(got) != 1 || got[0].index != 2 {
		t.Fatalf("filter partial = %+v", got)
	}
	if got := filterMenuItems(items, "zzz"); len(got) != 0 {
		t.Fatalf("filter none = %+v", got)
	}
}

func TestFormatContext(t *testing.T) {
	cases := map[int]string{
		0:       "0 context",
		128000:  "128k context",
		1048576: "1048.6k context",
		200000:  "200k context",
	}
	for in, want := range cases {
		if got := formatContext(in); got != want {
			t.Errorf("formatContext(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestDisplayWidth(t *testing.T) {
	cases := map[string]int{
		"":                  0,
		"hello":             5,
		"héllo":             5,
		"\x1b[36mab\x1b[0m": 2,
		"a\x1b[1mb\x1b[0mc": 3,
		"\x1b[2Kplain":      5,
	}
	for in, want := range cases {
		if got := displayWidth(in); got != want {
			t.Errorf("displayWidth(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestTruncateANSI(t *testing.T) {
	if got := truncateANSI("abcdefgh", 5); got != "abcd…" {
		t.Errorf("plain truncate = %q", got)
	}
	if got := truncateANSI("abc", 5); got != "abc" {
		t.Errorf("short unchanged = %q", got)
	}
	if got := truncateANSI("\x1b[36mabcdefgh\x1b[0m", 5); got != "\x1b[36mabcd…\x1b[0m" {
		t.Errorf("ansi truncate = %q", got)
	}
	if got := truncateANSI("\x1b[36mabc\x1b[0m", 5); got != "\x1b[36mabc\x1b[0m" {
		t.Errorf("ansi short unchanged = %q", got)
	}
	if got := truncateANSI("abc", 1); got != "…" {
		t.Errorf("width 1 = %q", got)
	}
	if got := truncateANSI("abc", 0); got != "" {
		t.Errorf("width 0 = %q", got)
	}
	if got := displayWidth(truncateANSI("\x1b[36mabcdefgh\x1b[0m", 5)); got != 5 {
		t.Errorf("truncated display width = %d, want 5", got)
	}
}

func TestCapPageSize(t *testing.T) {
	opts := menuOptions{Height: 20, PageSize: 12}
	capPageSize(&opts)
	if opts.PageSize != 12 {
		t.Errorf("height 20 page size = %d, want 12 (never raised)", opts.PageSize)
	}
	opts = menuOptions{Height: 20, PageSize: 20}
	capPageSize(&opts)
	if opts.PageSize != 14 {
		t.Errorf("height 20 page size = %d, want 14", opts.PageSize)
	}
	opts = menuOptions{Height: 7, PageSize: 12}
	capPageSize(&opts)
	if opts.PageSize != 1 {
		t.Errorf("height 7 page size = %d, want 1", opts.PageSize)
	}
	opts = menuOptions{Height: 0, PageSize: 12}
	capPageSize(&opts)
	if opts.PageSize != 12 {
		t.Errorf("height 0 page size = %d, want 12", opts.PageSize)
	}
}

func TestRenderMenuFitsWidth(t *testing.T) {
	longLabel := strings.Repeat("very-long-model-name/", 4) + "final"
	longDetail := "context window 1048576 max output 32768 free tier available with tools support"
	items := []indexedMenuItem{
		{item: menuItem{Label: longLabel, Detail: longDetail}, index: 0},
		{item: menuItem{Label: "short", Detail: "d"}, index: 1},
	}
	opts := menuOptions{Search: true, PageSize: 3, Width: 40, Height: 20}
	lines := renderMenu("Pick a model", items, "very", 0, 0, opts)
	for i, line := range lines {
		if w := displayWidth(line); w > 40 {
			t.Errorf("line %d width %d exceeds 40: %q", i, w, line)
		}
	}
	if len(lines) != 7 {
		t.Errorf("line count = %d, want 7 (title+search+blank+2 rows+blank+hint)", len(lines))
	}
	if !strings.Contains(lines[1], "search: very") {
		t.Errorf("search line = %q", lines[1])
	}
}
