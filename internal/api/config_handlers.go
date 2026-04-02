package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"

	"github.com/conflicthq/scuttlebot/internal/config"
)

// configView is the JSON shape returned by GET /v1/config.
// Secrets are masked — zero values mean "no change" on PUT.
type configView struct {
	APIAddr string                   `json:"api_addr"`
	MCPAddr string                   `json:"mcp_addr"`
	Bridge  bridgeConfigView         `json:"bridge"`
	Ergo    ergoConfigView           `json:"ergo"`
	TLS     tlsConfigView            `json:"tls"`
	LLM     llmConfigView            `json:"llm"`
	Topology config.TopologyConfig   `json:"topology"`
	History config.ConfigHistoryConfig `json:"config_history"`
}

type bridgeConfigView struct {
	Enabled           bool     `json:"enabled"`
	Nick              string   `json:"nick"`
	Channels          []string `json:"channels"`
	BufferSize        int      `json:"buffer_size"`
	WebUserTTLMinutes int      `json:"web_user_ttl_minutes"`
	// Password intentionally omitted — use PUT with non-empty value to change
}

type ergoConfigView struct {
	External    bool   `json:"external"`
	DataDir     string `json:"data_dir"`
	NetworkName string `json:"network_name"`
	ServerName  string `json:"server_name"`
	IRCAddr     string `json:"irc_addr"`
	// APIAddr and APIToken omitted (internal/secret)
}

type tlsConfigView struct {
	Domain        string `json:"domain"`
	Email         string `json:"email"`
	AllowInsecure bool   `json:"allow_insecure"`
}

type llmConfigView struct {
	Backends []llmBackendView `json:"backends"`
}

type llmBackendView struct {
	Name    string   `json:"name"`
	Backend string   `json:"backend"`
	BaseURL string   `json:"base_url,omitempty"`
	Model   string   `json:"model,omitempty"`
	Region  string   `json:"region,omitempty"`
	Allow   []string `json:"allow,omitempty"`
	Block   []string `json:"block,omitempty"`
	Default bool     `json:"default,omitempty"`
	// APIKey / AWSKeyID / AWSSecretKey omitted — blank = no change on PUT
}

func configToView(cfg config.Config) configView {
	backends := make([]llmBackendView, len(cfg.LLM.Backends))
	for i, b := range cfg.LLM.Backends {
		backends[i] = llmBackendView{
			Name:    b.Name,
			Backend: b.Backend,
			BaseURL: b.BaseURL,
			Model:   b.Model,
			Region:  b.Region,
			Allow:   b.Allow,
			Block:   b.Block,
			Default: b.Default,
		}
	}
	return configView{
		APIAddr: cfg.APIAddr,
		MCPAddr: cfg.MCPAddr,
		Bridge: bridgeConfigView{
			Enabled:           cfg.Bridge.Enabled,
			Nick:              cfg.Bridge.Nick,
			Channels:          cfg.Bridge.Channels,
			BufferSize:        cfg.Bridge.BufferSize,
			WebUserTTLMinutes: cfg.Bridge.WebUserTTLMinutes,
		},
		Ergo: ergoConfigView{
			External:    cfg.Ergo.External,
			DataDir:     cfg.Ergo.DataDir,
			NetworkName: cfg.Ergo.NetworkName,
			ServerName:  cfg.Ergo.ServerName,
			IRCAddr:     cfg.Ergo.IRCAddr,
		},
		TLS: tlsConfigView{
			Domain:        cfg.TLS.Domain,
			Email:         cfg.TLS.Email,
			AllowInsecure: cfg.TLS.AllowInsecure,
		},
		LLM:      llmConfigView{Backends: backends},
		Topology: cfg.Topology,
		History:  cfg.History,
	}
}

// handleGetConfig handles GET /v1/config.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfgStore.Get()
	writeJSON(w, http.StatusOK, configToView(cfg))
}

// configUpdateRequest is the body accepted by PUT /v1/config.
// Only the mutable, hot-reloadable sections. Restart-required fields (ergo IRC
// addr, TLS domain, api_addr) are accepted but flagged in the response.
type configUpdateRequest struct {
	Bridge   *bridgeConfigUpdate           `json:"bridge,omitempty"`
	Topology *config.TopologyConfig        `json:"topology,omitempty"`
	History  *config.ConfigHistoryConfig   `json:"config_history,omitempty"`
	LLM      *llmConfigUpdate              `json:"llm,omitempty"`
	// These fields trigger a restart_required notice but are still persisted.
	APIAddr *string `json:"api_addr,omitempty"`
	MCPAddr *string `json:"mcp_addr,omitempty"`
}

type bridgeConfigUpdate struct {
	Enabled           *bool    `json:"enabled,omitempty"`
	Nick              *string  `json:"nick,omitempty"`
	Channels          []string `json:"channels,omitempty"`
	BufferSize        *int     `json:"buffer_size,omitempty"`
	WebUserTTLMinutes *int     `json:"web_user_ttl_minutes,omitempty"`
	Password          *string  `json:"password,omitempty"` // blank = no change
}

type llmConfigUpdate struct {
	Backends []config.LLMBackendConfig `json:"backends"`
}

type configUpdateResponse struct {
	Saved           bool     `json:"saved"`
	RestartRequired []string `json:"restart_required,omitempty"`
}

// handlePutConfig handles PUT /v1/config.
func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var req configUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	next := s.cfgStore.Get()
	var restartRequired []string

	if req.Bridge != nil {
		b := req.Bridge
		if b.Enabled != nil {
			next.Bridge.Enabled = *b.Enabled
		}
		if b.Nick != nil {
			next.Bridge.Nick = *b.Nick
			restartRequired = appendUniq(restartRequired, "bridge.nick")
		}
		if b.Channels != nil {
			next.Bridge.Channels = b.Channels
		}
		if b.BufferSize != nil {
			next.Bridge.BufferSize = *b.BufferSize
		}
		if b.WebUserTTLMinutes != nil {
			next.Bridge.WebUserTTLMinutes = *b.WebUserTTLMinutes
		}
		if b.Password != nil && *b.Password != "" {
			next.Bridge.Password = *b.Password
			restartRequired = appendUniq(restartRequired, "bridge.password")
		}
	}

	if req.Topology != nil {
		next.Topology = *req.Topology
	}

	if req.History != nil {
		if req.History.Keep > 0 {
			next.History.Keep = req.History.Keep
		}
		if req.History.Dir != "" {
			next.History.Dir = req.History.Dir
		}
	}

	if req.LLM != nil {
		next.LLM.Backends = req.LLM.Backends
	}

	if req.APIAddr != nil && *req.APIAddr != "" {
		next.APIAddr = *req.APIAddr
		restartRequired = appendUniq(restartRequired, "api_addr")
	}
	if req.MCPAddr != nil && *req.MCPAddr != "" {
		next.MCPAddr = *req.MCPAddr
		restartRequired = appendUniq(restartRequired, "mcp_addr")
	}

	if err := s.cfgStore.Save(next); err != nil {
		s.log.Error("config save failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to save config")
		return
	}

	writeJSON(w, http.StatusOK, configUpdateResponse{
		Saved:           true,
		RestartRequired: restartRequired,
	})
}

// handleGetConfigHistory handles GET /v1/config/history.
func (s *Server) handleGetConfigHistory(w http.ResponseWriter, r *http.Request) {
	entries, err := s.cfgStore.ListHistory()
	if err != nil {
		s.log.Error("list config history", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// handleGetConfigHistoryEntry handles GET /v1/config/history/{filename}.
func (s *Server) handleGetConfigHistoryEntry(w http.ResponseWriter, r *http.Request) {
	filename := filepath.Base(r.PathValue("filename"))
	data, err := s.cfgStore.ReadHistoryFile(filename)
	if err != nil {
		writeError(w, http.StatusNotFound, "snapshot not found")
		return
	}
	// Parse snapshot to return as JSON (same masked view as GET /v1/config).
	var snapped config.Config
	snapped.Defaults()
	if err := snapped.LoadFromBytes(data); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to parse snapshot")
		return
	}
	writeJSON(w, http.StatusOK, configToView(snapped))
}

func appendUniq(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}
