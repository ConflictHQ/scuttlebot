package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/conflicthq/scuttlebot/internal/auth"
)

type createAPIKeyRequest struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresIn string   `json:"expires_in,omitempty"` // e.g. "720h" for 30 days, empty = never
	Team      string   `json:"team,omitempty"`       // optional team scope; empty = unrestricted
}

type createAPIKeyResponse struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Token     string       `json:"token"` // plaintext, shown only once
	Scopes    []auth.Scope `json:"scopes"`
	Team      string       `json:"team,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	ExpiresAt *time.Time   `json:"expires_at,omitempty"`
}

type apiKeyListEntry struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Scopes    []auth.Scope `json:"scopes"`
	Team      string       `json:"team,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	LastUsed  *time.Time   `json:"last_used,omitempty"`
	ExpiresAt *time.Time   `json:"expires_at,omitempty"`
	Active    bool         `json:"active"`
}

// handleListAPIKeys handles GET /v1/api-keys.
func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys := s.apiKeys.List()
	out := make([]apiKeyListEntry, len(keys))
	for i, k := range keys {
		out[i] = apiKeyListEntry{
			ID:        k.ID,
			Name:      k.Name,
			Scopes:    k.Scopes,
			Team:      k.Team,
			CreatedAt: k.CreatedAt,
			Active:    k.Active,
		}
		if !k.LastUsed.IsZero() {
			t := k.LastUsed
			out[i].LastUsed = &t
		}
		if !k.ExpiresAt.IsZero() {
			t := k.ExpiresAt
			out[i].ExpiresAt = &t
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateAPIKey handles POST /v1/api-keys.
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req createAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	scopes := make([]auth.Scope, len(req.Scopes))
	for i, s := range req.Scopes {
		scope := auth.Scope(s)
		if !auth.ValidScopes[scope] {
			writeError(w, http.StatusBadRequest, "unknown scope: "+s)
			return
		}
		scopes[i] = scope
	}
	if len(scopes) == 0 {
		writeError(w, http.StatusBadRequest, "at least one scope is required")
		return
	}

	var expiresAt time.Time
	if req.ExpiresIn != "" {
		dur, err := time.ParseDuration(req.ExpiresIn)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid expires_in duration: "+err.Error())
			return
		}
		expiresAt = time.Now().Add(dur)
	}

	token, key, err := s.apiKeys.Create(req.Name, scopes, expiresAt, req.Team)
	if err != nil {
		s.log.Error("create api key", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create API key")
		return
	}

	resp := createAPIKeyResponse{
		ID:        key.ID,
		Name:      key.Name,
		Token:     token,
		Scopes:    key.Scopes,
		Team:      key.Team,
		CreatedAt: key.CreatedAt,
	}
	if !key.ExpiresAt.IsZero() {
		t := key.ExpiresAt
		resp.ExpiresAt = &t
	}
	writeJSON(w, http.StatusCreated, resp)
}

// handleRevokeAPIKey handles DELETE /v1/api-keys/{id}.
func (s *Server) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.apiKeys.Revoke(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
