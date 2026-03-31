package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/conflicthq/scuttlebot/internal/bots/bridge"
)

// chatBridge is the interface the API layer requires from the bridge bot.
type chatBridge interface {
	Channels() []string
	JoinChannel(channel string)
	Messages(channel string) []bridge.Message
	Subscribe(channel string) (<-chan bridge.Message, func())
	Send(ctx context.Context, channel, text, senderNick string) error
}

func (s *Server) handleJoinChannel(w http.ResponseWriter, r *http.Request) {
	channel := "#" + r.PathValue("channel")
	s.bridge.JoinChannel(channel)
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
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	channel := "#" + r.PathValue("channel")
	var req struct {
		Text string `json:"text"`
		Nick string `json:"nick"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	if err := s.bridge.Send(r.Context(), channel, req.Text, req.Nick); err != nil {
		s.log.Error("bridge send", "channel", channel, "err", err)
		writeError(w, http.StatusInternalServerError, "send failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleChannelStream serves an SSE stream of IRC messages for a channel.
// Auth is via ?token= query param because EventSource doesn't support custom headers.
func (s *Server) handleChannelStream(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if _, ok := s.tokens[token]; !ok {
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
