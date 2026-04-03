package api

import (
	"net/http"

	"github.com/conflicthq/scuttlebot/internal/config"
)

type settingsResponse struct {
	TLS      tlsInfo  `json:"tls"`
	Policies Policies `json:"policies"`
}

type tlsInfo struct {
	Enabled       bool   `json:"enabled"`
	Domain        string `json:"domain,omitempty"`
	AllowInsecure bool   `json:"allow_insecure"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	resp := settingsResponse{
		TLS: tlsInfo{
			Enabled:       s.tlsDomain != "",
			Domain:        s.tlsDomain,
			AllowInsecure: true,
		},
	}
	if s.policies != nil {
		resp.Policies = s.policies.Get()
	}
	// Prefer ConfigStore for fields that have migrated to scuttlebot.yaml.
	if s.cfgStore != nil {
		cfg := s.cfgStore.Get()
		resp.Policies.AgentPolicy = toAPIAgentPolicy(cfg.AgentPolicy)
		resp.Policies.Logging = toAPILogging(cfg.Logging)
		resp.Policies.Bridge.WebUserTTLMinutes = cfg.Bridge.WebUserTTLMinutes
	}
	writeJSON(w, http.StatusOK, resp)
}

func toAPIAgentPolicy(c config.AgentPolicyConfig) AgentPolicy {
	return AgentPolicy{
		RequireCheckin:   c.RequireCheckin,
		CheckinChannel:   c.CheckinChannel,
		RequiredChannels: c.RequiredChannels,
	}
}

func toAPILogging(c config.LoggingConfig) LoggingPolicy {
	return LoggingPolicy{
		Enabled:    c.Enabled,
		Dir:        c.Dir,
		Format:     c.Format,
		Rotation:   c.Rotation,
		MaxSizeMB:  c.MaxSizeMB,
		PerChannel: c.PerChannel,
		MaxAgeDays: c.MaxAgeDays,
	}
}
