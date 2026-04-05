package api

import (
	"encoding/json"
	"net/http"

	"github.com/conflicthq/scuttlebot/internal/topology"
)

// TopologyManager is the interface the API server uses to provision channels
// and query the channel policy. Satisfied by *topology.Manager.
type TopologyManager = topologyManager
type topologyManager interface {
	ProvisionChannel(ch topology.ChannelConfig) error
	DropChannel(channel string)
	Policy() *topology.Policy
	GrantAccess(nick, channel, level string)
	RevokeAccess(nick, channel string)
	ListChannels() []topology.ChannelInfo
}

type provisionChannelRequest struct {
	Name         string   `json:"name"`
	Topic        string   `json:"topic,omitempty"`
	Ops          []string `json:"ops,omitempty"`
	Voice        []string `json:"voice,omitempty"`
	Autojoin     []string `json:"autojoin,omitempty"`
	Instructions string   `json:"instructions,omitempty"`
	MirrorDetail string   `json:"mirror_detail,omitempty"`
}

type provisionChannelResponse struct {
	Channel     string   `json:"channel"`
	Type        string   `json:"type,omitempty"`
	Supervision string   `json:"supervision,omitempty"`
	Autojoin    []string `json:"autojoin,omitempty"`
}

// handleProvisionChannel handles POST /v1/channels.
// It provisions an IRC channel via ChanServ, applies the autojoin policy for
// the channel's type, and returns the channel name, type, and supervision channel.
func (s *Server) handleProvisionChannel(w http.ResponseWriter, r *http.Request) {
	var req provisionChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := topology.ValidateName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.topoMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "topology not configured")
		return
	}

	policy := s.topoMgr.Policy()

	// Merge autojoin and modes from policy if the caller didn't specify any.
	autojoin := req.Autojoin
	if len(autojoin) == 0 && policy != nil {
		autojoin = policy.AutojoinFor(req.Name)
	}
	var modes []string
	if policy != nil {
		modes = policy.ModesFor(req.Name)
	}

	ch := topology.ChannelConfig{
		Name:     req.Name,
		Topic:    req.Topic,
		Ops:      req.Ops,
		Voice:    req.Voice,
		Autojoin: autojoin,
		Modes:    modes,
	}
	if err := s.topoMgr.ProvisionChannel(ch); err != nil {
		s.log.Error("provision channel", "channel", req.Name, "err", err)
		writeError(w, http.StatusInternalServerError, "provision failed")
		return
	}

	// Save instructions to policies if provided.
	if req.Instructions != "" && s.policies != nil {
		p := s.policies.Get()
		if p.OnJoinMessages == nil {
			p.OnJoinMessages = make(map[string]string)
		}
		p.OnJoinMessages[req.Name] = req.Instructions
		if req.MirrorDetail != "" {
			if p.Bridge.ChannelDisplay == nil {
				p.Bridge.ChannelDisplay = make(map[string]ChannelDisplayConfig)
			}
			cfg := p.Bridge.ChannelDisplay[req.Name]
			cfg.MirrorDetail = req.MirrorDetail
			p.Bridge.ChannelDisplay[req.Name] = cfg
		}
		_ = s.policies.Set(p)
	}

	resp := provisionChannelResponse{
		Channel:  req.Name,
		Autojoin: autojoin,
	}
	if policy != nil {
		resp.Type = policy.TypeName(req.Name)
		resp.Supervision = policy.SupervisionFor(req.Name)
	}
	writeJSON(w, http.StatusCreated, resp)
}

type channelTypeInfo struct {
	Name        string   `json:"name"`
	Prefix      string   `json:"prefix"`
	Autojoin    []string `json:"autojoin,omitempty"`
	Supervision string   `json:"supervision,omitempty"`
	Ephemeral   bool     `json:"ephemeral,omitempty"`
	TTLSeconds  int64    `json:"ttl_seconds,omitempty"`
}

type topologyResponse struct {
	StaticChannels []string               `json:"static_channels"`
	Types          []channelTypeInfo      `json:"types"`
	ActiveChannels []topology.ChannelInfo `json:"active_channels,omitempty"`
}

// handleDropChannel handles DELETE /v1/topology/channels/{channel}.
// Drops the ChanServ registration of an ephemeral channel.
func (s *Server) handleDropChannel(w http.ResponseWriter, r *http.Request) {
	channel := "#" + r.PathValue("channel")
	if err := topology.ValidateName(channel); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.topoMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "topology not configured")
		return
	}
	s.topoMgr.DropChannel(channel)
	w.WriteHeader(http.StatusNoContent)
}

// handleGetInstructions handles GET /v1/channels/{channel}/instructions.
func (s *Server) handleGetInstructions(w http.ResponseWriter, r *http.Request) {
	channel := "#" + r.PathValue("channel")
	if s.policies == nil {
		writeJSON(w, http.StatusOK, map[string]string{"channel": channel, "instructions": ""})
		return
	}
	p := s.policies.Get()
	msg := p.OnJoinMessages[channel]
	writeJSON(w, http.StatusOK, map[string]string{"channel": channel, "instructions": msg})
}

// handlePutInstructions handles PUT /v1/channels/{channel}/instructions.
func (s *Server) handlePutInstructions(w http.ResponseWriter, r *http.Request) {
	channel := "#" + r.PathValue("channel")
	if s.policies == nil {
		writeError(w, http.StatusServiceUnavailable, "policies not configured")
		return
	}
	var req struct {
		Instructions string `json:"instructions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	p := s.policies.Get()
	if p.OnJoinMessages == nil {
		p.OnJoinMessages = make(map[string]string)
	}
	p.OnJoinMessages[channel] = req.Instructions
	if err := s.policies.Set(p); err != nil {
		writeError(w, http.StatusInternalServerError, "save failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteInstructions handles DELETE /v1/channels/{channel}/instructions.
func (s *Server) handleDeleteInstructions(w http.ResponseWriter, r *http.Request) {
	channel := "#" + r.PathValue("channel")
	if s.policies == nil {
		writeError(w, http.StatusServiceUnavailable, "policies not configured")
		return
	}
	p := s.policies.Get()
	delete(p.OnJoinMessages, channel)
	if err := s.policies.Set(p); err != nil {
		writeError(w, http.StatusInternalServerError, "save failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetTopology handles GET /v1/topology.
// Returns the channel type rules and static channel names declared in config.
func (s *Server) handleGetTopology(w http.ResponseWriter, r *http.Request) {
	if s.topoMgr == nil {
		writeJSON(w, http.StatusOK, topologyResponse{})
		return
	}
	policy := s.topoMgr.Policy()
	if policy == nil {
		writeJSON(w, http.StatusOK, topologyResponse{})
		return
	}

	statics := policy.StaticChannels()
	staticNames := make([]string, len(statics))
	for i, sc := range statics {
		staticNames[i] = sc.Name
	}

	types := policy.Types()
	typeInfos := make([]channelTypeInfo, len(types))
	for i, t := range types {
		typeInfos[i] = channelTypeInfo{
			Name:        t.Name,
			Prefix:      t.Prefix,
			Autojoin:    t.Autojoin,
			Supervision: t.Supervision,
			Ephemeral:   t.Ephemeral,
			TTLSeconds:  int64(t.TTL.Seconds()),
		}
	}

	writeJSON(w, http.StatusOK, topologyResponse{
		StaticChannels: staticNames,
		Types:          typeInfos,
		ActiveChannels: s.topoMgr.ListChannels(),
	})
}
