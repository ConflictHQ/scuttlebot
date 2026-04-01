package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const geminiAPIBase = "https://generativelanguage.googleapis.com"

type geminiProvider struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
}

func newGeminiProvider(cfg BackendConfig, hc *http.Client) *geminiProvider {
	model := cfg.Model
	if model == "" {
		model = "gemini-1.5-flash"
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = geminiAPIBase
	}
	return &geminiProvider{
		apiKey:  cfg.APIKey,
		model:   model,
		baseURL: baseURL,
		http:    hc,
	}
}

func (p *geminiProvider) Summarize(ctx context.Context, prompt string) (string, error) {
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", p.baseURL, p.model, p.apiKey)
	body, _ := json.Marshal(map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]string{
					{"text": prompt},
				},
			},
		},
		"generationConfig": map[string]any{
			"maxOutputTokens": 512,
		},
	})

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini error %d: %s", resp.StatusCode, string(data))
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("gemini parse: %w", err)
	}
	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini returned no candidates")
	}
	return result.Candidates[0].Content.Parts[0].Text, nil
}

func (p *geminiProvider) DiscoverModels(ctx context.Context) ([]ModelInfo, error) {
	url := fmt.Sprintf("%s/v1beta/models?key=%s", p.baseURL, p.apiKey)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini models request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini models error %d: %s", resp.StatusCode, string(data))
	}

	var result struct {
		Models []struct {
			Name             string   `json:"name"`
			DisplayName      string   `json:"displayName"`
			Description      string   `json:"description"`
			SupportedMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("gemini models parse: %w", err)
	}

	var models []ModelInfo
	for _, m := range result.Models {
		// Only include models that support content generation.
		if !supportsGenerate(m.SupportedMethods) {
			continue
		}
		// Name is "models/gemini-1.5-flash" — strip the prefix.
		id := strings.TrimPrefix(m.Name, "models/")
		models = append(models, ModelInfo{
			ID:          id,
			Name:        m.DisplayName,
			Description: m.Description,
		})
	}
	return models, nil
}

func supportsGenerate(methods []string) bool {
	for _, m := range methods {
		if m == "generateContent" {
			return true
		}
	}
	return false
}
