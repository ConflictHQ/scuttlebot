// Package api implements the scuttlebot HTTP management API.
//
// /v1/ endpoints require a valid Bearer token.
// /ui/ is served unauthenticated (static web UI).
// /v1/channels/{channel}/stream uses ?token= query param (EventSource limitation).
package api

import (
	"log/slog"
	"net/http"

	"github.com/conflicthq/scuttlebot/internal/registry"
)

// Server is the scuttlebot HTTP API server.
type Server struct {
	registry *registry.Registry
	tokens   map[string]struct{}
	log      *slog.Logger
	bridge   chatBridge // nil if bridge is disabled
}

// New creates a new API Server. Pass nil for b to disable the chat bridge.
func New(reg *registry.Registry, tokens []string, b chatBridge, log *slog.Logger) *Server {
	tokenSet := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		tokenSet[t] = struct{}{}
	}
	return &Server{
		registry: reg,
		tokens:   tokenSet,
		log:      log,
		bridge:   b,
	}
}

// Handler returns the HTTP handler with all routes registered.
// /v1/ routes require a valid Bearer token. /ui/ is served unauthenticated.
func (s *Server) Handler() http.Handler {
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /v1/status", s.handleStatus)
	apiMux.HandleFunc("GET /v1/agents", s.handleListAgents)
	apiMux.HandleFunc("GET /v1/agents/{nick}", s.handleGetAgent)
	apiMux.HandleFunc("POST /v1/agents/register", s.handleRegister)
	apiMux.HandleFunc("POST /v1/agents/{nick}/rotate", s.handleRotate)
	apiMux.HandleFunc("POST /v1/agents/{nick}/revoke", s.handleRevoke)
	if s.bridge != nil {
		apiMux.HandleFunc("GET /v1/channels", s.handleListChannels)
		apiMux.HandleFunc("POST /v1/channels/{channel}/join", s.handleJoinChannel)
		apiMux.HandleFunc("GET /v1/channels/{channel}/messages", s.handleChannelMessages)
		apiMux.HandleFunc("POST /v1/channels/{channel}/messages", s.handleSendMessage)
	}

	outer := http.NewServeMux()
	outer.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})
	outer.Handle("/ui/", s.uiFileServer())
	// SSE stream uses ?token= auth (EventSource can't send headers), registered
	// on outer so it bypasses the Bearer-token authMiddleware on /v1/.
	if s.bridge != nil {
		outer.HandleFunc("GET /v1/channels/{channel}/stream", s.handleChannelStream)
	}
	outer.Handle("/v1/", s.authMiddleware(apiMux))

	return outer
}
