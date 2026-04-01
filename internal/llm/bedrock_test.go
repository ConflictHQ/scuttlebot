package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBedrockSummarize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST request, got %s", r.Method)
		}
		// Path: /model/{modelID}/converse
		if r.URL.Path != "/model/test-model/converse" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		resp := map[string]any{
			"output": map[string]any{
				"message": map[string]any{
					"content": []map[string]any{
						{"text": "bedrock response"},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p, _ := newBedrockProvider(BackendConfig{
		Backend:      "bedrock",
		Region:       "us-east-1",
		Model:        "test-model",
		BaseURL:      srv.URL,
		AWSKeyID:     "test-key",
		AWSSecretKey: "test-secret",
	}, srv.Client())

	got, err := p.Summarize(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}
	if got != "bedrock response" {
		t.Errorf("got %q, want %q", got, "bedrock response")
	}
}

func TestBedrockDiscoverModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/foundation-models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		resp := map[string]any{
			"modelSummaries": []map[string]any{
				{"modelId": "m1", "modelName": "Model 1"},
				{"modelId": "m2", "modelName": "Model 2"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p, _ := newBedrockProvider(BackendConfig{
		Backend:      "bedrock",
		Region:       "us-east-1",
		BaseURL:      srv.URL,
		AWSKeyID:     "test-key",
		AWSSecretKey: "test-secret",
	}, srv.Client())

	models, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels failed: %v", err)
	}

	if len(models) != 2 {
		t.Errorf("got %d models, want 2", len(models))
	}
}
