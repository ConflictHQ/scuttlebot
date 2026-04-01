package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const anthropicAPIBase = "https://api.anthropic.com"
const anthropicVersion = "2023-06-01"

// anthropicModels is a curated static list — Anthropic has no public list API.
var anthropicModels = []ModelInfo{
	{ID: "claude-opus-4-6", Name: "Claude Opus 4.6"},
	{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6"},
	{ID: "claude-haiku-4-5-20251001", Name: "Claude Haiku 4.5"},
	{ID: "claude-3-5-sonnet-20241022", Name: "Claude 3.5 Sonnet"},
	{ID: "claude-3-5-haiku-20241022", Name: "Claude 3.5 Haiku"},
	{ID: "claude-3-opus-20240229", Name: "Claude 3 Opus"},
	{ID: "claude-3-sonnet-20240229", Name: "Claude 3 Sonnet"},
	{ID: "claude-3-haiku-20240307", Name: "Claude 3 Haiku"},
}

type anthropicProvider struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
}

func newAnthropicProvider(cfg BackendConfig, hc *http.Client) *anthropicProvider {
	model := cfg.Model
	if model == "" {
		model = "claude-3-5-sonnet-20241022"
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = anthropicAPIBase
	}
	return &anthropicProvider{
		apiKey:  cfg.APIKey,
		model:   model,
		baseURL: baseURL,
		http:    hc,
	}
}

func (p *anthropicProvider) Summarize(ctx context.Context, prompt string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":      p.model,
		"max_tokens": 512,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic error %d: %s", resp.StatusCode, string(data))
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("anthropic parse: %w", err)
	}
	for _, c := range result.Content {
		if c.Type == "text" {
			return c.Text, nil
		}
	}
	return "", fmt.Errorf("anthropic returned no text content")
}

// DiscoverModels returns a curated static list (Anthropic has no public list API).
func (p *anthropicProvider) DiscoverModels(_ context.Context) ([]ModelInfo, error) {
	models := make([]ModelInfo, len(anthropicModels))
	copy(models, anthropicModels)
	return models, nil
}
