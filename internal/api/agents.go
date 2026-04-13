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
	Team        string                    `json:"team,omitempty"`
	Channels    []string                  `json:"channels"`
	OpsChannels []string                  `json:"ops_channels,omitempty"`
	Permissions []string                  `json:"permissions"`
	Skills      []string                  `json:"skills,omitempty"`
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

	// Set optional fields (team, skills) if provided.
	if req.Team != "" || len(req.Skills) > 0 {
		if agent, err := s.registry.Get(req.Nick); err == nil {
			if req.Team != "" {
				agent.Team = req.Team
			}
			if len(req.Skills) > 0 {
				agent.Skills = req.Skills
			}
			_ = s.registry.Update(agent)
		}
	}
	s.registry.Touch(req.Nick)
	go s.setAgentModes(req.Nick, req.Type, cfg) // async — don't block response
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

// handleBulkDeleteAgents handles POST /v1/agents/bulk-delete.
func (s *Server) handleBulkDeleteAgents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nicks []string `json:"nicks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Nicks) == 0 {
		writeError(w, http.StatusBadRequest, "nicks list is required")
		return
	}

	var deleted, failed int
	for _, nick := range req.Nicks {
		if agent, err := s.registry.Get(nick); err == nil {
			s.removeAgentModes(nick, agent.Channels)
		}
		if err := s.registry.Delete(nick); err != nil {
			s.log.Warn("bulk delete: failed", "nick", nick, "err", err)
			failed++
		} else {
			deleted++
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": deleted, "failed": failed})
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	nick := r.PathValue("nick")
	var req struct {
		Channels []string `json:"channels"`
		Team     *string  `json:"team,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Channels != nil {
		if err := s.registry.UpdateChannels(nick, req.Channels); err != nil {
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "revoked") {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			s.log.Error("update agent channels", "nick", nick, "err", err)
			writeError(w, http.StatusInternalServerError, "update failed")
			return
		}
	}
	if req.Team != nil {
		agent, err := s.registry.Get(nick)
		if err != nil {
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "revoked") {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			s.log.Error("update agent team", "nick", nick, "err", err)
			writeError(w, http.StatusInternalServerError, "update failed")
			return
		}
		agent.Team = *req.Team
		if err := s.registry.Update(agent); err != nil {
			s.log.Error("update agent team", "nick", nick, "err", err)
			writeError(w, http.StatusInternalServerError, "update failed")
			return
		}
	}
	s.registry.Touch(nick)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents := s.registry.List()

	// Team filtering: resolve the effective team from the API key scope and
	// the optional ?team= query param.
	agents = filterAgentsByTeam(agents, teamFromRequest(r), r.URL.Query().Get("team"))

	// Filter by skill if ?skill= query param is present.
	if skill := r.URL.Query().Get("skill"); skill != "" {
		filtered := make([]*registry.Agent, 0)
		for _, a := range agents {
			for _, s := range a.Skills {
				if strings.EqualFold(s, skill) {
					filtered = append(filtered, a)
					break
				}
			}
		}
		agents = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

// filterAgentsByTeam filters agents by team. keyTeam is the API key's team
// scope (empty = unrestricted), queryTeam is the ?team= query param.
//
// Rules:
//   - Unrestricted key + no query param: return all agents.
//   - Unrestricted key + query param: filter to agents matching that team.
//   - Team-scoped key + no query param: filter to agents matching key's team.
//   - Team-scoped key + same query param: same as above.
//   - Team-scoped key + different query param: empty result (cannot escape scope).
func filterAgentsByTeam(agents []*registry.Agent, keyTeam, queryTeam string) []*registry.Agent {
	effectiveTeam := keyTeam
	if queryTeam != "" {
		if keyTeam != "" && !strings.EqualFold(queryTeam, keyTeam) {
			// Team-scoped key cannot query a different team.
			return []*registry.Agent{}
		}
		effectiveTeam = queryTeam
	}
	if effectiveTeam == "" {
		return agents
	}

	filtered := make([]*registry.Agent, 0)
	for _, a := range agents {
		if strings.EqualFold(a.Team, effectiveTeam) {
			filtered = append(filtered, a)
		}
	}
	return filtered
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

// handleAgentBlocker handles POST /v1/agents/{nick}/blocker.
// Agents or relays call this to escalate that an agent is stuck.
func (s *Server) handleAgentBlocker(w http.ResponseWriter, r *http.Request) {
	nick := r.PathValue("nick")
	var req struct {
		Channel string `json:"channel,omitempty"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	alert := "[blocker] " + nick
	if req.Channel != "" {
		alert += " in " + req.Channel
	}
	alert += ": " + req.Message

	// Post to #ops if bridge is available.
	if s.bridge != nil {
		_ = s.bridge.Send(r.Context(), "#ops", alert, "")
	}
	s.log.Warn("agent blocker", "nick", nick, "channel", req.Channel, "message", req.Message)
	w.WriteHeader(http.StatusNoContent)
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
