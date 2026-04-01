package llm

import (
	"context"
	"fmt"
	"net/http"
)

// KnownBackends maps OpenAI-compatible backend names to their default base URLs.
var KnownBackends = map[string]string{
	"openai":      "https://api.openai.com/v1",
	"openrouter":  "https://openrouter.ai/api/v1",
	"together":    "https://api.together.xyz/v1",
	"groq":        "https://api.groq.com/openai/v1",
	"fireworks":   "https://api.fireworks.ai/inference/v1",
	"mistral":     "https://api.mistral.ai/v1",
	"ai21":        "https://api.ai21.com/studio/v1",
	"huggingface": "https://api-inference.huggingface.co/v1",
	"deepseek":    "https://api.deepseek.com/v1",
	"cerebras":    "https://api.cerebras.ai/v1",
	"xai":         "https://api.x.ai/v1",
	// Local / self-hosted (defaults — override with base_url)
	"litellm":     "http://localhost:4000/v1",
	"lmstudio":    "http://localhost:1234/v1",
	"jan":         "http://localhost:1337/v1",
	"localai":     "http://localhost:8080/v1",
	"vllm":        "http://localhost:8000/v1",
	"anythingllm": "http://localhost:3001/v1",
}

// New creates a Provider from the given config. The returned value may also
// implement ModelDiscoverer — check with a type assertion before calling
// DiscoverModels. Allow/block filters in cfg are applied transparently by
// wrapping the discoverer.
func New(cfg BackendConfig) (Provider, error) {
	hc := &http.Client{}
	switch cfg.Backend {
	case "anthropic":
		return newAnthropicProvider(cfg, hc), nil

	case "gemini":
		return newGeminiProvider(cfg, hc), nil

	case "bedrock":
		return newBedrockProvider(cfg, hc)

	case "ollama":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		return newOllamaProvider(cfg, baseURL, hc), nil

	default:
		// OpenAI-compatible backend.
		baseURL := cfg.BaseURL
		if baseURL == "" {
			u, ok := KnownBackends[cfg.Backend]
			if !ok {
				return nil, fmt.Errorf("llm: unknown backend %q — set base_url for custom endpoints", cfg.Backend)
			}
			baseURL = u
		}
		model := cfg.Model
		if model == "" {
			model = "gpt-4o-mini"
		}
		return newOpenAIProvider(cfg.APIKey, baseURL, model, hc), nil
	}
}

// Discover runs model discovery for the given config, applying any allow/block
// filters from the config. Returns an error if the provider doesn't support
// discovery.
func Discover(ctx context.Context, cfg BackendConfig) ([]ModelInfo, error) {
	p, err := New(cfg)
	if err != nil {
		return nil, err
	}
	d, ok := p.(ModelDiscoverer)
	if !ok {
		return nil, fmt.Errorf("llm: backend %q does not support model discovery", cfg.Backend)
	}
	models, err := d.DiscoverModels(ctx)
	if err != nil {
		return nil, err
	}
	if len(cfg.Allow) > 0 || len(cfg.Block) > 0 {
		f, ferr := NewModelFilter(cfg.Allow, cfg.Block)
		if ferr != nil {
			return nil, ferr
		}
		models = f.Apply(models)
	}
	return models, nil
}

// BackendNames returns a sorted list of all known backend names.
func BackendNames() []string {
	names := make([]string, 0, len(KnownBackends)+4)
	for k := range KnownBackends {
		names = append(names, k)
	}
	names = append(names, "anthropic", "gemini", "bedrock", "ollama")
	return names
}
