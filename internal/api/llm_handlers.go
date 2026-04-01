package api

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/conflicthq/scuttlebot/internal/config"
	"github.com/conflicthq/scuttlebot/internal/llm"
)

// backendView is the read-safe representation of a backend returned by the API.
// API keys are replaced with "***" if set, so they are never exposed.
type backendView struct {
	Name     string   `json:"name"`
	Backend  string   `json:"backend"`
	APIKey   string   `json:"api_key,omitempty"` // "***" if set, "" if not
	BaseURL  string   `json:"base_url,omitempty"`
	Model    string   `json:"model,omitempty"`
	Region   string   `json:"region,omitempty"`
	AWSKeyID string   `json:"aws_key_id,omitempty"` // "***" if set
	Allow    []string `json:"allow,omitempty"`
	Block    []string `json:"block,omitempty"`
	Default  bool     `json:"default,omitempty"`
	Source   string   `json:"source"` // "config" (yaml, read-only) or "policy" (ui-managed)
}

func mask(s string) string {
	if s == "" {
		return ""
	}
	return "***"
}

// handleLLMKnown returns the list of all known backend names.
func (s *Server) handleLLMKnown(w http.ResponseWriter, _ *http.Request) {
	type knownBackend struct {
		Name    string `json:"name"`
		BaseURL string `json:"base_url,omitempty"`
		Native  bool   `json:"native,omitempty"`
	}

	var backends []knownBackend
	for name, url := range llm.KnownBackends {
		backends = append(backends, knownBackend{Name: name, BaseURL: url})
	}
	backends = append(backends,
		knownBackend{Name: "anthropic", Native: true},
		knownBackend{Name: "gemini", Native: true},
		knownBackend{Name: "bedrock", Native: true},
		knownBackend{Name: "ollama", BaseURL: "http://localhost:11434", Native: true},
	)
	sort.Slice(backends, func(i, j int) bool {
		return backends[i].Name < backends[j].Name
	})
	writeJSON(w, http.StatusOK, backends)
}

// handleLLMBackends lists all configured backends (YAML config + policy store).
// API keys are masked.
func (s *Server) handleLLMBackends(w http.ResponseWriter, _ *http.Request) {
	var out []backendView

	// YAML-configured backends (read-only).
	if s.llmCfg != nil {
		for _, b := range s.llmCfg.Backends {
			out = append(out, backendView{
				Name:     b.Name,
				Backend:  b.Backend,
				APIKey:   mask(b.APIKey),
				BaseURL:  b.BaseURL,
				Model:    b.Model,
				Region:   b.Region,
				AWSKeyID: mask(b.AWSKeyID),
				Allow:    b.Allow,
				Block:    b.Block,
				Default:  b.Default,
				Source:   "config",
			})
		}
	}

	// Policy-store backends (UI-managed, editable).
	if s.policies != nil {
		for _, b := range s.policies.Get().LLMBackends {
			out = append(out, backendView{
				Name:     b.Name,
				Backend:  b.Backend,
				APIKey:   mask(b.APIKey),
				BaseURL:  b.BaseURL,
				Model:    b.Model,
				Region:   b.Region,
				AWSKeyID: mask(b.AWSKeyID),
				Allow:    b.Allow,
				Block:    b.Block,
				Default:  b.Default,
				Source:   "policy",
			})
		}
	}

	if out == nil {
		out = []backendView{}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleLLMBackendCreate adds a new backend to the policy store.
func (s *Server) handleLLMBackendCreate(w http.ResponseWriter, r *http.Request) {
	if s.policies == nil {
		http.Error(w, "policy store not available", http.StatusServiceUnavailable)
		return
	}
	var b PolicyLLMBackend
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if b.Name == "" || b.Backend == "" {
		http.Error(w, "name and backend are required", http.StatusBadRequest)
		return
	}

	p := s.policies.Get()
	for _, existing := range p.LLMBackends {
		if existing.Name == b.Name {
			http.Error(w, "backend name already exists", http.StatusConflict)
			return
		}
	}
	// Also check YAML backends.
	if s.llmCfg != nil {
		for _, existing := range s.llmCfg.Backends {
			if existing.Name == b.Name {
				http.Error(w, "backend name already exists in config", http.StatusConflict)
				return
			}
		}
	}

	p.LLMBackends = append(p.LLMBackends, b)
	if err := s.policies.Set(p); err != nil {
		http.Error(w, "failed to save", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, backendView{
		Name:    b.Name,
		Backend: b.Backend,
		APIKey:  mask(b.APIKey),
		BaseURL: b.BaseURL,
		Model:   b.Model,
		Region:  b.Region,
		Allow:   b.Allow,
		Block:   b.Block,
		Default: b.Default,
		Source:  "policy",
	})
}

// handleLLMBackendUpdate updates a policy-store backend by name.
// Fields present in the request body override the stored value.
// Send api_key / aws_secret_key as "" to leave the stored value unchanged
// (the UI masks these and should omit them if unchanged).
func (s *Server) handleLLMBackendUpdate(w http.ResponseWriter, r *http.Request) {
	if s.policies == nil {
		http.Error(w, "policy store not available", http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("name")
	var req PolicyLLMBackend
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	p := s.policies.Get()
	idx := -1
	for i, b := range p.LLMBackends {
		if b.Name == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		http.Error(w, "backend not found (only policy backends are editable)", http.StatusNotFound)
		return
	}

	existing := p.LLMBackends[idx]
	// Preserve stored secrets when the UI sends "***" or empty.
	if req.APIKey == "" || req.APIKey == "***" {
		req.APIKey = existing.APIKey
	}
	if req.AWSSecretKey == "" || req.AWSSecretKey == "***" {
		req.AWSSecretKey = existing.AWSSecretKey
	}
	if req.AWSKeyID == "" || req.AWSKeyID == "***" {
		req.AWSKeyID = existing.AWSKeyID
	}
	req.Name = name // name is immutable
	p.LLMBackends[idx] = req

	if err := s.policies.Set(p); err != nil {
		http.Error(w, "failed to save", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, backendView{
		Name:    req.Name,
		Backend: req.Backend,
		APIKey:  mask(req.APIKey),
		BaseURL: req.BaseURL,
		Model:   req.Model,
		Region:  req.Region,
		Allow:   req.Allow,
		Block:   req.Block,
		Default: req.Default,
		Source:  "policy",
	})
}

// handleLLMBackendDelete removes a policy-store backend by name.
func (s *Server) handleLLMBackendDelete(w http.ResponseWriter, r *http.Request) {
	if s.policies == nil {
		http.Error(w, "policy store not available", http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("name")
	p := s.policies.Get()
	idx := -1
	for i, b := range p.LLMBackends {
		if b.Name == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		http.Error(w, "backend not found (only policy backends are deletable)", http.StatusNotFound)
		return
	}
	p.LLMBackends = append(p.LLMBackends[:idx], p.LLMBackends[idx+1:]...)
	if err := s.policies.Set(p); err != nil {
		http.Error(w, "failed to save", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleLLMModels runs model discovery for the named backend.
// Looks in YAML config first, then policy store.
func (s *Server) handleLLMModels(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg, ok := s.findBackendConfig(name)
	if !ok {
		http.Error(w, "backend not found", http.StatusNotFound)
		return
	}
	models, err := llm.Discover(r.Context(), cfg)
	if err != nil {
		s.log.Error("llm model discovery", "backend", name, "err", err)
		http.Error(w, "model discovery failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, models)
}

// handleLLMDiscover runs ad-hoc model discovery from form credentials.
// Used by the UI "load live models" button before a backend is saved.
func (s *Server) handleLLMDiscover(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Backend      string   `json:"backend"`
		APIKey       string   `json:"api_key"`
		BaseURL      string   `json:"base_url"`
		Model        string   `json:"model"`
		Region       string   `json:"region"`
		AWSKeyID     string   `json:"aws_key_id"`
		AWSSecretKey string   `json:"aws_secret_key"`
		Allow        []string `json:"allow"`
		Block        []string `json:"block"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Backend == "" {
		http.Error(w, "backend is required", http.StatusBadRequest)
		return
	}
	cfg := llm.BackendConfig{
		Backend:      req.Backend,
		APIKey:       req.APIKey,
		BaseURL:      req.BaseURL,
		Model:        req.Model,
		Region:       req.Region,
		AWSKeyID:     req.AWSKeyID,
		AWSSecretKey: req.AWSSecretKey,
		Allow:        req.Allow,
		Block:        req.Block,
	}
	models, err := llm.Discover(r.Context(), cfg)
	if err != nil {
		s.log.Error("llm ad-hoc discovery", "backend", req.Backend, "err", err)
		http.Error(w, "model discovery failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, models)
}

// findBackendConfig looks up a backend by name in YAML config then policy store.
func (s *Server) findBackendConfig(name string) (llm.BackendConfig, bool) {
	if s.llmCfg != nil {
		for _, b := range s.llmCfg.Backends {
			if b.Name == name {
				return yamlBackendToLLM(b), true
			}
		}
	}
	if s.policies != nil {
		for _, b := range s.policies.Get().LLMBackends {
			if b.Name == name {
				return policyBackendToLLM(b), true
			}
		}
	}
	return llm.BackendConfig{}, false
}

func yamlBackendToLLM(b config.LLMBackendConfig) llm.BackendConfig {
	return llm.BackendConfig{
		Backend:      b.Backend,
		APIKey:       b.APIKey,
		BaseURL:      b.BaseURL,
		Model:        b.Model,
		Region:       b.Region,
		AWSKeyID:     b.AWSKeyID,
		AWSSecretKey: b.AWSSecretKey,
		Allow:        b.Allow,
		Block:        b.Block,
	}
}

// handleLLMComplete proxies a prompt to a named backend and returns the text.
// The API key stays server-side — callers only need a Bearer token.
//
// POST /v1/llm/complete
//
//	{"backend": "anthro", "prompt": "hello"}
//	→ {"text": "..."}
func (s *Server) handleLLMComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Backend string `json:"backend"`
		Prompt  string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Backend == "" || req.Prompt == "" {
		http.Error(w, "backend and prompt are required", http.StatusBadRequest)
		return
	}

	cfg, ok := s.findBackendConfig(req.Backend)
	if !ok {
		http.Error(w, "backend not found", http.StatusNotFound)
		return
	}

	provider, err := llm.New(cfg)
	if err != nil {
		http.Error(w, "backend init failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	text, err := provider.Summarize(r.Context(), req.Prompt)
	if err != nil {
		s.log.Error("llm complete", "backend", req.Backend, "err", err)
		http.Error(w, "llm error: "+err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

func policyBackendToLLM(b PolicyLLMBackend) llm.BackendConfig {
	return llm.BackendConfig{
		Backend:      b.Backend,
		APIKey:       b.APIKey,
		BaseURL:      b.BaseURL,
		Model:        b.Model,
		Region:       b.Region,
		AWSKeyID:     b.AWSKeyID,
		AWSSecretKey: b.AWSSecretKey,
		Allow:        b.Allow,
		Block:        b.Block,
	}
}
