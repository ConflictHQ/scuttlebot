package oracle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// OpenAIProvider calls any OpenAI-compatible chat completion API.
// Works with OpenAI, Anthropic (via compatibility layer), local Ollama, etc.
type OpenAIProvider struct {
	BaseURL string // e.g. "https://api.openai.com/v1"
	APIKey  string
	Model   string
	http    *http.Client
}

// NewOpenAIProvider creates a provider from environment variables:
//
//	ORACLE_OPENAI_BASE_URL (default: https://api.openai.com/v1)
//	ORACLE_OPENAI_API_KEY  (required)
//	ORACLE_OPENAI_MODEL    (default: gpt-4o-mini)
func NewOpenAIProvider() *OpenAIProvider {
	baseURL := os.Getenv("ORACLE_OPENAI_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	model := os.Getenv("ORACLE_OPENAI_MODEL")
	if model == "" {
		model = "gpt-4o-mini"
	}
	return &OpenAIProvider{
		BaseURL: baseURL,
		APIKey:  os.Getenv("ORACLE_OPENAI_API_KEY"),
		Model:   model,
		http:    &http.Client{},
	}
}

// Summarize calls the chat completions endpoint with the given prompt.
func (p *OpenAIProvider) Summarize(ctx context.Context, prompt string) (string, error) {
	if p.APIKey == "" {
		return "", fmt.Errorf("ORACLE_OPENAI_API_KEY is not set")
	}

	body, _ := json.Marshal(map[string]any{
		"model": p.Model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens": 512,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openai error %d: %s", resp.StatusCode, string(data))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("openai parse: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("openai returned no choices")
	}
	return result.Choices[0].Message.Content, nil
}

// StubProvider returns a fixed summary. Used in tests and when no LLM is configured.
type StubProvider struct {
	Response string
	Err      error
}

func (s *StubProvider) Summarize(_ context.Context, _ string) (string, error) {
	return s.Response, s.Err
}
