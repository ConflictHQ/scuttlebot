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
	OpsChannels []string                  `json:"ops_channels,omitempty"`
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
		OpsChannels: req.OpsChannels,
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

	s.registry.Touch(req.Nick)
	s.setAgentModes(req.Nick, req.Type, cfg)
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
		OpsChannels []string           `json:"ops_channels,omitempty"`
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
		OpsChannels: req.OpsChannels,
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
	s.setAgentModes(nick, req.Type, cfg)
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
	// Look up agent channels before revoking so we can remove access.
	if agent, err := s.registry.Get(nick); err == nil {
		s.removeAgentModes(nick, agent.Channels)
	}
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
	// Look up agent channels before deleting so we can remove access.
	if agent, err := s.registry.Get(nick); err == nil {
		s.removeAgentModes(nick, agent.Channels)
	}
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

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	nick := r.PathValue("nick")
	var req struct {
		Channels []string `json:"channels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.registry.UpdateChannels(nick, req.Channels); err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "revoked") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		s.log.Error("update agent channels", "nick", nick, "err", err)
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	s.registry.Touch(nick)
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

// agentModeLevel maps an agent type to the ChanServ access level it should
// receive. Returns "" for types that get no special mode.
func agentModeLevel(t registry.AgentType) string {
	switch t {
	case registry.AgentTypeOperator, registry.AgentTypeOrchestrator:
		return "OP"
	case registry.AgentTypeWorker:
		return "VOICE"
	default:
		return ""
	}
}

// setAgentModes grants the appropriate ChanServ access for an agent on all
// its assigned channels based on its type. For orchestrators with OpsChannels
// configured, +o is granted only on those channels and +v on the rest.
// No-op when topology is not configured or the agent type doesn't warrant a mode.
func (s *Server) setAgentModes(nick string, agentType registry.AgentType, cfg registry.EngagementConfig) {
	if s.topoMgr == nil {
		return
	}
	level := agentModeLevel(agentType)
	if level == "" {
		return
	}

	// Orchestrators with explicit OpsChannels get +o only on those channels
	// and +v on remaining channels.
	if level == "OP" && len(cfg.OpsChannels) > 0 {
		opsSet := make(map[string]struct{}, len(cfg.OpsChannels))
		for _, ch := range cfg.OpsChannels {
			opsSet[ch] = struct{}{}
		}
		for _, ch := range cfg.Channels {
			if _, isOps := opsSet[ch]; isOps {
				s.topoMgr.GrantAccess(nick, ch, "OP")
			} else {
				s.topoMgr.GrantAccess(nick, ch, "VOICE")
			}
		}
		return
	}

	for _, ch := range cfg.Channels {
		s.topoMgr.GrantAccess(nick, ch, level)
	}
}

// removeAgentModes revokes ChanServ access for an agent on all its assigned
// channels. No-op when topology is not configured.
func (s *Server) removeAgentModes(nick string, channels []string) {
	if s.topoMgr == nil {
		return
	}
	for _, ch := range channels {
		s.topoMgr.RevokeAccess(nick, ch)
	}
}
