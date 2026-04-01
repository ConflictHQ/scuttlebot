package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type ollamaProvider struct {
	baseURL string
	model   string
	http    *http.Client
}

func newOllamaProvider(cfg BackendConfig, baseURL string, hc *http.Client) *ollamaProvider {
	model := cfg.Model
	if model == "" {
		model = "llama3.2"
	}
	return &ollamaProvider{
		baseURL: baseURL,
		model:   model,
		http:    hc,
	}
}

func (p *ollamaProvider) Summarize(ctx context.Context, prompt string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":  p.model,
		"prompt": prompt,
		"stream": false,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama error %d: %s", resp.StatusCode, string(data))
	}

	var result struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("ollama parse: %w", err)
	}
	return result.Response, nil
}

// DiscoverModels calls the Ollama /api/tags endpoint to list installed models.
func (p *ollamaProvider) DiscoverModels(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama models request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama models error %d: %s", resp.StatusCode, string(data))
	}

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("ollama models parse: %w", err)
	}

	models := make([]ModelInfo, len(result.Models))
	for i, m := range result.Models {
		models[i] = ModelInfo{ID: m.Name, Name: m.Name}
	}
	return models, nil
}
