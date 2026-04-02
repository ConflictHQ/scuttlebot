package api

import (
	"encoding/json"
	"net/http"

	"github.com/conflicthq/scuttlebot/internal/topology"
)

// topologyManager is the interface the API server uses to provision channels
// and query the channel policy. Satisfied by *topology.Manager.
type topologyManager interface {
	ProvisionChannel(ch topology.ChannelConfig) error
	DropChannel(channel string)
	Policy() *topology.Policy
}

type provisionChannelRequest struct {
	Name     string   `json:"name"`
	Topic    string   `json:"topic,omitempty"`
	Ops      []string `json:"ops,omitempty"`
	Voice    []string `json:"voice,omitempty"`
	Autojoin []string `json:"autojoin,omitempty"`
}

type provisionChannelResponse struct {
	Channel    string   `json:"channel"`
	Type       string   `json:"type,omitempty"`
	Supervision string  `json:"supervision,omitempty"`
	Autojoin   []string `json:"autojoin,omitempty"`
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

	policy := s.topoMgr.Policy()

	// Merge autojoin from policy if the caller didn't specify any.
	autojoin := req.Autojoin
	if len(autojoin) == 0 && policy != nil {
		autojoin = policy.AutojoinFor(req.Name)
	}

	ch := topology.ChannelConfig{
		Name:     req.Name,
		Topic:    req.Topic,
		Ops:      req.Ops,
		Voice:    req.Voice,
		Autojoin: autojoin,
	}
	if err := s.topoMgr.ProvisionChannel(ch); err != nil {
		s.log.Error("provision channel", "channel", req.Name, "err", err)
		writeError(w, http.StatusInternalServerError, "provision failed")
		return
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
	StaticChannels []string          `json:"static_channels"`
	Types          []channelTypeInfo `json:"types"`
}

// handleDropChannel handles DELETE /v1/topology/channels/{channel}.
// Drops the ChanServ registration of an ephemeral channel.
func (s *Server) handleDropChannel(w http.ResponseWriter, r *http.Request) {
	channel := "#" + r.PathValue("channel")
	if err := topology.ValidateName(channel); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.topoMgr.DropChannel(channel)
	w.WriteHeader(http.StatusNoContent)
}

// handleGetTopology handles GET /v1/topology.
// Returns the channel type rules and static channel names declared in config.
func (s *Server) handleGetTopology(w http.ResponseWriter, r *http.Request) {
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
	})
}
