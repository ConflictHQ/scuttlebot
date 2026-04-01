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

// openAIProvider implements Provider and ModelDiscoverer for any OpenAI-compatible API.
type openAIProvider struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

func newOpenAIProvider(apiKey, baseURL, model string, hc *http.Client) *openAIProvider {
	return &openAIProvider{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		http:    hc,
	}
}

func (p *openAIProvider) Summarize(ctx context.Context, prompt string) (string, error) {
	text, status, data, err := p.summarizeWithTokenField(ctx, prompt, "max_tokens")
	if err == nil {
		return text, nil
	}
	if shouldRetryWithMaxCompletionTokens(status, data) {
		text, _, _, err := p.summarizeWithTokenField(ctx, prompt, "max_completion_tokens")
		return text, err
	}
	return "", err
}

func (p *openAIProvider) summarizeWithTokenField(ctx context.Context, prompt, tokenField string) (string, int, []byte, error) {
	body, _ := json.Marshal(map[string]any{
		"model": p.model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		tokenField: 512,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", 0, nil, err
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(req)
	if err != nil {
		return "", 0, nil, fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, data, fmt.Errorf("openai error %d: %s", resp.StatusCode, string(data))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", resp.StatusCode, data, fmt.Errorf("openai parse: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", resp.StatusCode, data, fmt.Errorf("openai returned no choices")
	}
	return result.Choices[0].Message.Content, resp.StatusCode, data, nil
}

func shouldRetryWithMaxCompletionTokens(status int, data []byte) bool {
	if status != http.StatusBadRequest {
		return false
	}
	var result struct {
		Error struct {
			Message string `json:"message"`
			Param   string `json:"param"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &result); err == nil {
		if result.Error.Param == "max_tokens" && strings.Contains(strings.ToLower(result.Error.Message), "not supported") {
			return true
		}
	}
	lower := strings.ToLower(string(data))
	return strings.Contains(lower, "unsupported parameter") && strings.Contains(lower, "max_tokens")
}

func (p *openAIProvider) DiscoverModels(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("models request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models error %d: %s", resp.StatusCode, string(data))
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("models parse: %w", err)
	}

	models := make([]ModelInfo, len(result.Data))
	for i, m := range result.Data {
		models[i] = ModelInfo{ID: m.ID}
	}
	return models, nil
}
