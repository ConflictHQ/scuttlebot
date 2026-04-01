package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGeminiSummarize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST request, got %s", r.Method)
		}
		if r.URL.Path != "/v1beta/models/gemini-1.5-flash:generateContent" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("key") != "test-api-key" {
			t.Errorf("expected api key test-api-key, got %s", r.URL.Query().Get("key"))
		}

		resp := map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"parts": []map[string]any{
							{"text": "gemini response"},
						},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := newGeminiProvider(BackendConfig{
		Backend: "gemini",
		APIKey:  "test-api-key",
		BaseURL: srv.URL,
	}, srv.Client())

	got, err := p.Summarize(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}
	if got != "gemini response" {
		t.Errorf("got %q, want %q", got, "gemini response")
	}
}

func TestGeminiDiscoverModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/v1beta/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		resp := map[string]any{
			"models": []map[string]any{
				{
					"name":                       "models/gemini-1.5-flash",
					"displayName":                "Gemini 1.5 Flash",
					"description":                "Fast and versatile",
					"supportedGenerationMethods": []string{"generateContent"},
				},
				{
					"name":                       "models/other-model",
					"displayName":                "Other",
					"description":                "Other model",
					"supportedGenerationMethods": []string{"other"},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := newGeminiProvider(BackendConfig{
		Backend: "gemini",
		APIKey:  "test-api-key",
		BaseURL: srv.URL,
	}, srv.Client())

	models, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels failed: %v", err)
	}

	if len(models) != 1 {
		t.Errorf("got %d models, want 1", len(models))
	}
	if models[0].ID != "gemini-1.5-flash" {
		t.Errorf("got ID %q, want %q", models[0].ID, "gemini-1.5-flash")
	}
}
