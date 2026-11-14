package cli

import (
	"testing"

	"claude-custom/internal/config"
)

func testConfigWithProfiles() *config.Config {
	return &config.Config{
		DefaultProfile: "groq",
		Profiles: map[string]config.Profile{
			"groq": {
				Provider: "groq",
				Model:    "qwen",
				BaseURL:  "http://127.0.0.1:1/v1",
			},
			"openrouter": {
				Provider: "openrouter",
				Model:    "nemotron",
				BaseURL:  "http://127.0.0.1:2/v1",
			},
			"openrouter_alt": {
				Provider: "openrouter",
				Model:    "gemma",
				BaseURL:  "http://127.0.0.1:2/v1",
			},
		},
		Providers: map[string]config.Provider{
			"groq":       {Preset: "groq"},
			"openrouter": {Preset: "openrouter"},
		},
	}
}

func TestResolveFallbacksFallsOverToOtherConfiguredProfiles(t *testing.T) {
	cfg := testConfigWithProfiles()
	profile, _, err := cfg.Resolve("groq")
	if err != nil {
		t.Fatal(err)
	}
	chain, err := resolveFallbacks(cfg, "groq", profile)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, target := range chain {
		got = append(got, target.Name+"/"+target.Model)
	}
	want := []string{"openrouter/nemotron", "openrouter_alt/gemma"}
	if len(got) != len(want) {
		t.Fatalf("chain = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chain = %v, want %v (other providers first, then more models of the same provider)", got, want)
		}
	}
}

func TestResolveFallbacksPrefersExplicitThenOtherProviderThenSameProvider(t *testing.T) {
	cfg := testConfigWithProfiles()
	cfg.Profiles["openrouter"] = config.Profile{
		Provider:  "openrouter",
		Model:     "nemotron",
		BaseURL:   "http://127.0.0.1:2/v1",
		Fallbacks: []string{"openrouter/cohere:free"},
	}
	profile, _, err := cfg.Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	chain, err := resolveFallbacks(cfg, "openrouter", profile)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, target := range chain {
		got = append(got, target.Name+"/"+target.Model)
	}
	want := []string{"openrouter/cohere:free", "groq/qwen", "openrouter_alt/gemma"}
	if len(got) != len(want) {
		t.Fatalf("chain = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chain = %v, want %v", got, want)
		}
	}
}

func TestResolveFallbacksSkipsDuplicates(t *testing.T) {
	cfg := testConfigWithProfiles()
	cfg.Profiles["openrouter"] = config.Profile{
		Provider:  "openrouter",
		Model:     "nemotron",
		BaseURL:   "http://127.0.0.1:2/v1",
		Fallbacks: []string{"openrouter_alt/gemma"},
	}
	profile, _, err := cfg.Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	chain, err := resolveFallbacks(cfg, "openrouter", profile)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, target := range chain {
		seen[target.Name+"/"+target.Model]++
	}
	if seen["openrouter_alt/gemma"] != 1 {
		t.Fatalf("gemma appears %d times in %v", seen["openrouter_alt/gemma"], chain)
	}
}
