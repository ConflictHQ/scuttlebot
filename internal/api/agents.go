package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/conflicthq/scuttlebot/internal/registry"
	"github.com/conflicthq/scuttlebot/pkg/ircagent"
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
	team, ok := effectiveRegisterTeam(teamFromRequest(r), req.Team)
	if !ok {
		writeError(w, http.StatusForbidden, "team outside scope")
		return
	}
	if !s.requireScopedChannels(w, r, req.Channels) || !s.requireScopedChannels(w, r, req.OpsChannels) {
		return
	}
	req.Team = team

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
	creds, _, err := s.registry.Register(req.Nick, req.Type, cfg)
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
		agent, err := s.registry.Get(req.Nick)
		if err != nil {
			s.log.Error("register agent metadata", "nick", req.Nick, "err", err)
			writeError(w, http.StatusInternalServerError, "registration failed")
			return
		}
		if req.Team != "" {
			agent.Team = req.Team
		}
		if len(req.Skills) > 0 {
			agent.Skills = req.Skills
		}
		if err := s.registry.Update(agent); err != nil {
			s.log.Error("register agent metadata", "nick", req.Nick, "err", err)
			writeError(w, http.StatusInternalServerError, "registration failed")
			return
		}
	}

	payload, err := s.registry.SignedPayload(req.Nick)
	if err != nil {
		s.log.Error("register payload", "nick", req.Nick, "err", err)
		writeError(w, http.StatusInternalServerError, "registration failed")
		return
	}
	s.registry.Touch(req.Nick)
	go s.setAgentModes(req.Nick, req.Type, cfg)
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
	if !s.requireScopedChannels(w, r, req.Channels) || !s.requireScopedChannels(w, r, req.OpsChannels) {
		return
	}
	cfg := registry.EngagementConfig{
		Channels:    req.Channels,
		OpsChannels: req.OpsChannels,
		Permissions: req.Permissions,
	}
	_, err := s.registry.Adopt(nick, req.Type, cfg)
	if err != nil {
		if strings.Contains(err.Error(), "already registered") {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		s.log.Error("adopt agent", "nick", nick, "err", err)
		writeError(w, http.StatusInternalServerError, "adopt failed")
		return
	}
	if team := teamFromRequest(r); team != "" {
		agent, err := s.registry.Get(nick)
		if err != nil {
			s.log.Error("adopt agent metadata", "nick", nick, "err", err)
			writeError(w, http.StatusInternalServerError, "adopt failed")
			return
		}
		agent.Team = team
		if err := s.registry.Update(agent); err != nil {
			s.log.Error("adopt agent metadata", "nick", nick, "err", err)
			writeError(w, http.StatusInternalServerError, "adopt failed")
			return
		}
	}
	payload, err := s.registry.SignedPayload(nick)
	if err != nil {
		s.log.Error("adopt payload", "nick", nick, "err", err)
		writeError(w, http.StatusInternalServerError, "adopt failed")
		return
	}
	go s.setAgentModes(nick, req.Type, cfg)
	writeJSON(w, http.StatusOK, map[string]any{"nick": nick, "payload": payload})
}

func (s *Server) handleRotate(w http.ResponseWriter, r *http.Request) {
	nick := r.PathValue("nick")
	if _, ok := s.getScopedAgent(w, r, nick); !ok {
		return
	}
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
	agent, ok := s.getScopedAgent(w, r, nick)
	if !ok {
		return
	}
	go s.removeAgentModes(nick, agent.Channels)
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
	agent, ok := s.getScopedAgent(w, r, nick)
	if !ok {
		return
	}
	go s.removeAgentModes(nick, agent.Channels)
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
		agent, err := s.registry.Get(nick)
		if err != nil || !agentAllowedForTeam(agent, teamFromRequest(r)) {
			failed++
			continue
		}
		go s.removeAgentModes(nick, agent.Channels)
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
	agent, ok := s.getScopedAgent(w, r, nick)
	if !ok {
		return
	}
	var req struct {
		Channels []string `json:"channels"`
		Team     *string  `json:"team,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Channels != nil {
		if !s.requireScopedChannels(w, r, req.Channels) {
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
	}
	if req.Team != nil {
		team, ok := effectiveUpdatedTeam(teamFromRequest(r), req.Team)
		if !ok {
			writeError(w, http.StatusForbidden, "team outside scope")
			return
		}
		agent.Team = team
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
	agent, ok := s.getScopedAgent(w, r, nick)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

// handleAgentBlocker handles POST /v1/agents/{nick}/blocker.
// Agents or relays call this to escalate that an agent is stuck.
func (s *Server) handleAgentBlocker(w http.ResponseWriter, r *http.Request) {
	nick := r.PathValue("nick")
	if _, ok := s.getScopedAgent(w, r, nick); !ok {
		return
	}
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

// agentModeLevel maps an agent type to the default ChanServ access level it
// should receive on every assigned channel. Operators (humans) get +o
// everywhere; agents — including orchestrators — get +v everywhere by default.
// Orchestrators that need +o on specific channels declare them via
// EngagementConfig.OpsChannels; setAgentModes grants OP per-channel for that
// allowlist. Returns "" for types that get no special mode.
//
// Hard rule (operator-flagged): nicks matching a known agent prefix
// (claude-, codex-, gemini-, openclaw-) NEVER receive +o regardless of the
// configured type. Only humans get ops. This guards against accidentally
// (or intentionally) registering an automation as type=operator.
func agentModeLevel(t registry.AgentType) string {
	switch t {
	case registry.AgentTypeOperator:
		return "OP"
	case registry.AgentTypeOrchestrator, registry.AgentTypeWorker:
		return "VOICE"
	default:
		return ""
	}
}

// isAgentPrefixedNick reports whether the nick looks like a relay session
// (claude-, codex-, gemini-, openclaw-, …). Used to enforce the
// "agents-never-op" rule independently of the registered AgentType.
func isAgentPrefixedNick(nick string) bool {
	return ircagent.HasAnyPrefix(nick, ircagent.DefaultActivityPrefixes())
}

// setAgentModes reconciles the ChanServ AMODE entries for an agent so they
// match the current agentModeLevel policy. Each call revokes any prior
// AMODE for the nick on each channel and grants the correct level — making
// the function idempotent and self-healing for stale grants written under
// older policy (e.g. orchestrator-was-OP entries that should now be +v).
// No-op when topology is not configured or the agent type doesn't warrant
// a mode.
//
// Hard rule: nicks matching a relay-agent prefix (claude-, codex-, gemini-,
// openclaw-) are pinned to VOICE regardless of the requested level. Only
// human operators get ops.
func (s *Server) setAgentModes(nick string, agentType registry.AgentType, cfg registry.EngagementConfig) {
	if s.topoMgr == nil {
		return
	}
	level := agentModeLevel(agentType)
	if level == "" {
		return
	}
	// Agents never get +o regardless of configured type.
	if isAgentPrefixedNick(nick) && level == "OP" {
		level = "VOICE"
		if s.log != nil {
			s.log.Info("api: agent-prefixed nick capped to VOICE", "nick", nick)
		}
	}

	// Orchestrators may opt in to +o on a per-channel allowlist via
	// OpsChannels — but only for non-agent-prefixed nicks.
	if agentType == registry.AgentTypeOrchestrator && len(cfg.OpsChannels) > 0 && !isAgentPrefixedNick(nick) {
		opsSet := make(map[string]struct{}, len(cfg.OpsChannels))
		for _, ch := range cfg.OpsChannels {
			opsSet[ch] = struct{}{}
		}
		for _, ch := range cfg.Channels {
			s.topoMgr.RevokeAccess(nick, ch)
			if _, isOps := opsSet[ch]; isOps {
				s.topoMgr.GrantAccess(nick, ch, "OP")
			} else {
				s.topoMgr.GrantAccess(nick, ch, "VOICE")
			}
		}
		return
	}

	for _, ch := range cfg.Channels {
		s.topoMgr.RevokeAccess(nick, ch)
		s.topoMgr.GrantAccess(nick, ch, level)
	}

	// If the agent is currently joined to any of these channels with a
	// stale +o, AMODE alone won't take effect until they part+rejoin.
	// Issue a live MODE -o so the running session loses ops immediately.
	// Prefer the topology manager — once a channel is ChanServ-registered
	// (#177), the topology bot is founder and has authoritative +o.
	// Bridge is a fallback for channels not yet registered: it's the
	// first joiner of unregistered Ergo channels and gets transient +o.
	if level != "OP" {
		mode, hasMode := s.topoMgr.(channelLiveModeSetter)
		for _, ch := range cfg.Channels {
			if hasMode {
				mode.SetChannelMode(ch, "-o", nick)
			} else if s.bridge != nil {
				s.bridge.SetMode(ch, "-o", nick)
			}
		}
	}
}

// channelLiveModeSetter is the optional interface a topology manager can
// implement to set channel modes on already-joined sessions (in addition
// to ChanServ AMODE which only applies on next join). When the manager
// doesn't implement it, the live mode catch-up is skipped — the AMODE
// state alone will take effect on the agent's next reconnect.
type channelLiveModeSetter interface {
	SetChannelMode(channel, mode, target string)
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

// ReconcileAgentModes walks every registered agent and reapplies setAgentModes
// so AMODE entries match the current agentModeLevel policy. Used at daemon
// startup to clear stale +o grants written under an older policy (e.g.
// orchestrator-was-OP entries that should now be +v). Safe to call any time.
func (s *Server) ReconcileAgentModes() {
	if s.topoMgr == nil || s.registry == nil {
		return
	}
	agents := s.registry.List()
	for _, a := range agents {
		if a == nil || a.Revoked {
			continue
		}
		s.setAgentModes(a.Nick, a.Type, a.Config)
	}
	if s.log != nil {
		s.log.Info("api: reconciled agent modes", "count", len(agents))
	}
}
