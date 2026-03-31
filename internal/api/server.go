// Package api implements the scuttlebot HTTP management API.
//
// All endpoints require a valid Bearer token. No anonymous access.
// Agents and external systems use this API to register, manage credentials,
// and query fleet status.
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
}

// New creates a new API Server.
func New(reg *registry.Registry, tokens []string, log *slog.Logger) *Server {
	tokenSet := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		tokenSet[t] = struct{}{}
	}
	return &Server{
		registry: reg,
		tokens:   tokenSet,
		log:      log,
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

	outer := http.NewServeMux()
	outer.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})
	outer.Handle("/ui/", s.uiFileServer())
	outer.Handle("/v1/", s.authMiddleware(apiMux))

	return outer
}
