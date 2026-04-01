package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicSummarize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST request, got %s", r.Method)
		}
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-api-key" {
			t.Errorf("expected api key test-api-key, got %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("expected anthropic-version 2023-06-01, got %s", r.Header.Get("anthropic-version"))
		}

		resp := map[string]any{
			"content": []map[string]any{
				{
					"type": "text",
					"text": "anthropic response",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := newAnthropicProvider(BackendConfig{
		Backend: "anthropic",
		APIKey:  "test-api-key",
		BaseURL: srv.URL,
	}, srv.Client())

	got, err := p.Summarize(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}
	if got != "anthropic response" {
		t.Errorf("got %q, want %q", got, "anthropic response")
	}
}

func TestAnthropicDiscoverModels(t *testing.T) {
	p := newAnthropicProvider(BackendConfig{
		Backend: "anthropic",
		APIKey:  "test-api-key",
	}, http.DefaultClient)

	models, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels failed: %v", err)
	}

	if len(models) == 0 {
		t.Error("expected non-empty model list")
	}
	found := false
	for _, m := range models {
		if m.ID == "claude-3-5-sonnet-20241022" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find claude-3-5-sonnet-20241022 in model list")
	}
}
