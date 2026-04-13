package api

import (
	"net/http"
	"strings"

	"github.com/conflicthq/scuttlebot/internal/registry"
	"github.com/conflicthq/scuttlebot/internal/topology"
)

func normalizeScopedChannel(channel string) string {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return ""
	}
	if !strings.HasPrefix(channel, "#") {
		channel = "#" + channel
	}
	return strings.ToLower(channel)
}

func channelAllowedForTeam(channel, keyTeam string, shared []string) bool {
	keyTeam = strings.TrimSpace(keyTeam)
	if keyTeam == "" {
		return true
	}

	channel = normalizeScopedChannel(channel)
	if channel == "" {
		return false
	}

	for _, sharedChannel := range shared {
		if channel == normalizeScopedChannel(sharedChannel) {
			return true
		}
	}

	if !strings.HasPrefix(channel, "#team-") {
		return true
	}

	ownPrefix := "#team-" + strings.ToLower(keyTeam) + "-"
	return strings.HasPrefix(channel, ownPrefix)
}

func agentAllowedForTeam(agent *registry.Agent, keyTeam string) bool {
	keyTeam = strings.TrimSpace(keyTeam)
	return keyTeam == "" || strings.EqualFold(agent.Team, keyTeam)
}

func effectiveRegisterTeam(keyTeam, requestedTeam string) (string, bool) {
	keyTeam = strings.TrimSpace(keyTeam)
	requestedTeam = strings.TrimSpace(requestedTeam)
	if keyTeam == "" {
		return requestedTeam, true
	}
	if requestedTeam == "" || strings.EqualFold(requestedTeam, keyTeam) {
		return keyTeam, true
	}
	return "", false
}

func effectiveUpdatedTeam(keyTeam string, requestedTeam *string) (string, bool) {
	if requestedTeam == nil {
		return "", true
	}
	keyTeam = strings.TrimSpace(keyTeam)
	team := strings.TrimSpace(*requestedTeam)
	if keyTeam == "" {
		return team, true
	}
	if team == "" || !strings.EqualFold(team, keyTeam) {
		return "", false
	}
	return keyTeam, true
}

func filterChannelInfosByTeam(channels []topology.ChannelInfo, keyTeam string, shared []string) []topology.ChannelInfo {
	if keyTeam == "" {
		return channels
	}
	filtered := make([]topology.ChannelInfo, 0, len(channels))
	for _, channel := range channels {
		if channelAllowedForTeam(channel.Name, keyTeam, shared) {
			filtered = append(filtered, channel)
		}
	}
	return filtered
}

func (s *Server) sharedChannels() []string {
	if s.cfgStore == nil {
		return nil
	}
	return append([]string(nil), s.cfgStore.Get().Topology.SharedChannels...)
}

func (s *Server) requestAllowsChannel(r *http.Request, channel string) bool {
	return channelAllowedForTeam(channel, teamFromRequest(r), s.sharedChannels())
}

func (s *Server) requireScopedChannel(w http.ResponseWriter, r *http.Request, channel string) bool {
	if s.requestAllowsChannel(r, channel) {
		return true
	}
	writeError(w, http.StatusNotFound, "channel not found")
	return false
}

func (s *Server) requireScopedChannels(w http.ResponseWriter, r *http.Request, channels []string) bool {
	for _, channel := range channels {
		if channelAllowedForTeam(channel, teamFromRequest(r), s.sharedChannels()) {
			continue
		}
		writeError(w, http.StatusForbidden, "channel outside team scope")
		return false
	}
	return true
}

func (s *Server) getScopedAgent(w http.ResponseWriter, r *http.Request, nick string) (*registry.Agent, bool) {
	agent, err := s.registry.Get(nick)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return nil, false
	}
	if agentAllowedForTeam(agent, teamFromRequest(r)) {
		return agent, true
	}
	writeError(w, http.StatusNotFound, "agent not found")
	return nil, false
}
