package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/conflicthq/scuttlebot/internal/auth"
	"github.com/conflicthq/scuttlebot/internal/bots/bridge"
)

// chatBridge is the interface the API layer requires from the bridge bot.
type chatBridge interface {
	Channels() []string
	JoinChannel(channel string)
	LeaveChannel(channel string)
	Messages(channel string) []bridge.Message
	Subscribe(channel string) (<-chan bridge.Message, func())
	Send(ctx context.Context, channel, text, senderNick string) error
	SendWithMeta(ctx context.Context, channel, text, senderNick string, meta *bridge.Meta) error
	Stats() bridge.Stats
	TouchUser(channel, nick string)
	Users(channel string) []string
	UsersWithModes(channel string) []bridge.UserInfo
	ChannelModes(channel string) string
}

func (s *Server) handleJoinChannel(w http.ResponseWriter, r *http.Request) {
	channel := "#" + r.PathValue("channel")
	s.bridge.JoinChannel(channel)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	channel := "#" + r.PathValue("channel")
	s.bridge.LeaveChannel(channel)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"channels": s.bridge.Channels()})
}

func (s *Server) handleChannelMessages(w http.ResponseWriter, r *http.Request) {
	channel := "#" + r.PathValue("channel")
	// Auto-join on first access so the bridge starts tracking this channel.
	s.bridge.JoinChannel(channel)
	msgs := s.bridge.Messages(channel)
	if msgs == nil {
		msgs = []bridge.Message{}
	}
	// Filter by ?since=<RFC3339> when provided (avoids sending full history on each poll).
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		since, err := time.Parse(time.RFC3339Nano, sinceStr)
		if err == nil {
			filtered := msgs[:0]
			for _, m := range msgs {
				if m.At.After(since) {
					filtered = append(filtered, m)
				}
			}
			msgs = filtered
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	channel := "#" + r.PathValue("channel")
	var req struct {
		Text string       `json:"text"`
		Nick string       `json:"nick"`
		Meta *bridge.Meta `json:"meta,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	if err := s.bridge.SendWithMeta(r.Context(), channel, req.Text, req.Nick, req.Meta); err != nil {
		s.log.Error("bridge send", "channel", channel, "err", err)
		writeError(w, http.StatusInternalServerError, "send failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleChannelPresence(w http.ResponseWriter, r *http.Request) {
	channel := "#" + r.PathValue("channel")
	var req struct {
		Nick string `json:"nick"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Nick == "" {
		writeError(w, http.StatusBadRequest, "nick is required")
		return
	}
	s.bridge.TouchUser(channel, req.Nick)
	if s.registry != nil {
		s.registry.Touch(req.Nick)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleChannelUsers(w http.ResponseWriter, r *http.Request) {
	channel := "#" + r.PathValue("channel")
	users := s.bridge.UsersWithModes(channel)
	if users == nil {
		users = []bridge.UserInfo{}
	}
	modes := s.bridge.ChannelModes(channel)
	writeJSON(w, http.StatusOK, map[string]any{"users": users, "channel_modes": modes})
}

func (s *Server) handleGetChannelConfig(w http.ResponseWriter, r *http.Request) {
	channel := "#" + r.PathValue("channel")
	if s.policies == nil {
		writeJSON(w, http.StatusOK, ChannelDisplayConfig{})
		return
	}
	p := s.policies.Get()
	cfg := p.Bridge.ChannelDisplay[channel]
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handlePutChannelConfig(w http.ResponseWriter, r *http.Request) {
	channel := "#" + r.PathValue("channel")
	if s.policies == nil {
		writeError(w, http.StatusServiceUnavailable, "policies not configured")
		return
	}
	var cfg ChannelDisplayConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	p := s.policies.Get()
	if p.Bridge.ChannelDisplay == nil {
		p.Bridge.ChannelDisplay = make(map[string]ChannelDisplayConfig)
	}
	p.Bridge.ChannelDisplay[channel] = cfg
	if err := s.policies.Set(p); err != nil {
		writeError(w, http.StatusInternalServerError, "save failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleChannelStream serves an SSE stream of IRC messages for a channel.
// Auth is via ?token= query param because EventSource doesn't support custom headers.
func (s *Server) handleChannelStream(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	key := s.apiKeys.Lookup(token)
	if key == nil || (!key.HasScope(auth.ScopeChannels) && !key.HasScope(auth.ScopeChat)) {
		writeError(w, http.StatusUnauthorized, "invalid or missing token")
		return
	}

	channel := "#" + r.PathValue("channel")
	s.bridge.JoinChannel(channel)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	msgs, unsub := s.bridge.Subscribe(channel)
	defer unsub()

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-msgs:
			if !ok {
				return
			}
			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}
