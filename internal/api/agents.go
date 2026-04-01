package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/conflicthq/scuttlebot/internal/registry"
)

type registerRequest struct {
	Nick        string                    `json:"nick"`
	Type        registry.AgentType        `json:"type"`
	Channels    []string                  `json:"channels"`
	Permissions []string                  `json:"permissions"`
	RateLimit   *registry.RateLimitConfig `json:"rate_limit,omitempty"`
	Rules       *registry.EngagementRules `json:"engagement,omitempty"`
}

type registerResponse struct {
	Credentials *registry.Credentials   `json:"credentials"`
	Payload     *registry.SignedPayload `json:"payload"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Nick == "" {
		writeError(w, http.StatusBadRequest, "nick is required")
		return
	}
	if req.Type == "" {
		req.Type = registry.AgentTypeWorker
	}

	cfg := registry.EngagementConfig{
		Channels:    req.Channels,
		Permissions: req.Permissions,
	}
	if req.RateLimit != nil {
		cfg.RateLimit = *req.RateLimit
	}
	if req.Rules != nil {
		cfg.Rules = *req.Rules
	}
	creds, payload, err := s.registry.Register(req.Nick, req.Type, cfg)
	if err != nil {
		if strings.Contains(err.Error(), "already registered") {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		s.log.Error("register agent", "nick", req.Nick, "err", err)
		writeError(w, http.StatusInternalServerError, "registration failed")
		return
	}

	writeJSON(w, http.StatusCreated, registerResponse{
		Credentials: creds,
		Payload:     payload,
	})
}

func (s *Server) handleAdopt(w http.ResponseWriter, r *http.Request) {
	nick := r.PathValue("nick")
	var req struct {
		Type        registry.AgentType `json:"type"`
		Channels    []string           `json:"channels"`
		Permissions []string           `json:"permissions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Type == "" {
		req.Type = registry.AgentTypeWorker
	}
	cfg := registry.EngagementConfig{
		Channels:    req.Channels,
		Permissions: req.Permissions,
	}
	payload, err := s.registry.Adopt(nick, req.Type, cfg)
	if err != nil {
		if strings.Contains(err.Error(), "already registered") {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		s.log.Error("adopt agent", "nick", nick, "err", err)
		writeError(w, http.StatusInternalServerError, "adopt failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nick": nick, "payload": payload})
}

func (s *Server) handleRotate(w http.ResponseWriter, r *http.Request) {
	nick := r.PathValue("nick")
	creds, err := s.registry.Rotate(nick)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "revoked") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		s.log.Error("rotate credentials", "nick", nick, "err", err)
		writeError(w, http.StatusInternalServerError, "rotation failed")
		return
	}
	writeJSON(w, http.StatusOK, creds)
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	nick := r.PathValue("nick")
	if err := s.registry.Revoke(nick); err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "revoked") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		s.log.Error("revoke agent", "nick", nick, "err", err)
		writeError(w, http.StatusInternalServerError, "revocation failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	nick := r.PathValue("nick")
	if err := s.registry.Delete(nick); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		s.log.Error("delete agent", "nick", nick, "err", err)
		writeError(w, http.StatusInternalServerError, "deletion failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents := s.registry.List()
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	nick := r.PathValue("nick")
	agent, err := s.registry.Get(nick)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, agent)
}
