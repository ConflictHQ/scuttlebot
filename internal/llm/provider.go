// Package llm is the omnibus LLM gateway — any bot or service can use it to
// call language models without depending on a specific provider's SDK.
//
// Usage:
//
//	p, err := llm.New(llm.BackendConfig{Backend: "anthropic", APIKey: "sk-ant-..."})
//	text, err := p.Summarize(ctx, prompt)
//
// Model discovery (if the provider implements ModelDiscoverer):
//
//	if d, ok := p.(llm.ModelDiscoverer); ok {
//	    models, err := d.DiscoverModels(ctx)
//	}
package llm

import "context"

// Provider calls a language model to generate text.
// All provider implementations satisfy this interface.
type Provider interface {
	Summarize(ctx context.Context, prompt string) (string, error)
}

// ModelInfo describes a model returned by discovery.
type ModelInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ModelDiscoverer can enumerate available models for a backend.
// Providers that support live model listing implement this interface.
type ModelDiscoverer interface {
	DiscoverModels(ctx context.Context) ([]ModelInfo, error)
}
