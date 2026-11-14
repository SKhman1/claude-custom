package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAttributionHeaders(t *testing.T) {
	got := AttributionHeaders("https://openrouter.ai/api/v1", map[string]string{"X-Custom": "1"})
	if got["HTTP-Referer"] == "" || got["X-Title"] == "" {
		t.Fatalf("openrouter headers = %v", got)
	}
	if got["X-Custom"] != "1" {
		t.Fatalf("custom header lost: %v", got)
	}
	if len(AttributionHeaders("https://api.groq.com/openai/v1", nil)) != 0 {
		t.Fatal("non-openrouter provider got attribution headers")
	}
	own := map[string]string{"http-referer": "https://mine.example"}
	if AttributionHeaders("https://openrouter.ai/api/v1", own)["X-Title"] != "" {
		t.Fatal("caller-supplied header should not be overridden")
	}
}

func TestFilterChat(t *testing.T) {
	in := []Model{
		{ID: "allam-2-7b", Name: "ALLaM-2-7b"},
		{ID: "canopylabs/orpheus-arabic-saudi", Name: "Canopy Labs Orpheus Arabic Saudi"},
		{ID: "meta-llama/llama-prompt-guard-2-22m", Name: "Llama Prompt Guard 2 22M"},
		{ID: "openai/gpt-oss-120b", Name: "GPT OSS 120B"},
		{ID: "nvidia/canary-1b-flash", Name: "Canary (speech) 1B Flash"},
		{ID: "playai/tts-v3", Name: "PlayAI TTS v3"},
	}
	got := FilterChat(in)
	want := []string{"allam-2-7b", "openai/gpt-oss-120b", "nvidia/canary-1b-flash"}
	if len(got) != len(want) {
		t.Fatalf("FilterChat returned %d models, want %d: %+v", len(got), len(want), got)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("FilterChat[%d] = %q, want %q", i, got[i].ID, id)
		}
	}
}

func TestFetchModelsWithHeaders(t *testing.T) {
	var gotAuth, gotReferer, gotAccept, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotReferer = r.Header.Get("HTTP-Referer")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"b-model","display_name":"B Model","context_length":32000,"max_completion_tokens":4096},
			{"id":"a-model","name":"A Model"},
			{"id":"a-model","name":"duplicate"}
		]}`))
	}))
	defer srv.Close()

	models, err := FetchModelsWithHeaders(context.Background(), srv.URL+"/v1", "test-key", map[string]string{"HTTP-Referer": "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotReferer != "https://example.com" {
		t.Fatalf("custom header = %q", gotReferer)
	}
	if gotAccept != "application/json" {
		t.Fatalf("accept = %q", gotAccept)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ID != "a-model" || models[0].Name != "A Model" {
		t.Fatalf("models[0] = %+v", models[0])
	}
	if models[1].ID != "b-model" || models[1].Name != "B Model" || models[1].ContextWindow != 32000 || models[1].MaxOutput != 4096 {
		t.Fatalf("models[1] = %+v", models[1])
	}
}

func TestFetchModelsWrapsHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"id":"m1"}]}`))
	}))
	defer srv.Close()

	models, err := FetchModels(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "m1" || models[0].Name != "m1" {
		t.Fatalf("models = %+v", models)
	}
}

func TestFetchModelsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, err := FetchModels(context.Background(), srv.URL+"/v1", "k"); err == nil {
		t.Fatal("expected error for non-2xx response")
	}
	if _, err := FetchModels(context.Background(), "   ", "k"); err == nil {
		t.Fatal("expected error for empty base URL")
	}
}
