package llm

import (
	"testing"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		cfg     BackendConfig
		wantErr bool
	}{
		{
			name: "openai",
			cfg:  BackendConfig{Backend: "openai", APIKey: "key"},
		},
		{
			name: "anthropic",
			cfg:  BackendConfig{Backend: "anthropic", APIKey: "key"},
		},
		{
			name: "gemini",
			cfg:  BackendConfig{Backend: "gemini", APIKey: "key"},
		},
		{
			name: "ollama",
			cfg:  BackendConfig{Backend: "ollama", BaseURL: "http://localhost:11434"},
		},
		{
			name: "bedrock",
			cfg:  BackendConfig{Backend: "bedrock", Region: "us-east-1", AWSKeyID: "key", AWSSecretKey: "secret"},
		},
		{
			name:    "unknown",
			cfg:     BackendConfig{Backend: "unknown"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBackendNames(t *testing.T) {
	names := BackendNames()
	if len(names) == 0 {
		t.Error("expected non-empty backend names")
	}

	foundGemini := false
	for _, n := range names {
		if n == "gemini" {
			foundGemini = true
			break
		}
	}
	if !foundGemini {
		t.Error("expected gemini in backend names")
	}
}

func TestKnownBackends(t *testing.T) {
	if _, ok := KnownBackends["openai"]; !ok {
		t.Error("expected openai in known backends")
	}
}
