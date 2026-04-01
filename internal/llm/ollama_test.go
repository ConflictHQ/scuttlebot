package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaSummarize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST request, got %s", r.Method)
		}
		if r.URL.Path != "/api/generate" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var req struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		resp := map[string]any{
			"response": "ollama response",
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := newOllamaProvider(BackendConfig{
		Backend: "ollama",
		Model:   "test-model",
	}, srv.URL, srv.Client())

	got, err := p.Summarize(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}
	if got != "ollama response" {
		t.Errorf("got %q, want %q", got, "ollama response")
	}
}

func TestOllamaDiscoverModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/api/tags" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		resp := map[string]any{
			"models": []map[string]any{
				{"name": "model1"},
				{"name": "model2"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := newOllamaProvider(BackendConfig{
		Backend: "ollama",
	}, srv.URL, srv.Client())

	models, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels failed: %v", err)
	}

	if len(models) != 2 {
		t.Errorf("got %d models, want 2", len(models))
	}
}
