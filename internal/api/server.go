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
// Auth middleware wraps every route — no endpoint is reachable without a valid token.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("GET /v1/agents", s.handleListAgents)
	mux.HandleFunc("GET /v1/agents/{nick}", s.handleGetAgent)
	mux.HandleFunc("POST /v1/agents/register", s.handleRegister)
	mux.HandleFunc("POST /v1/agents/{nick}/rotate", s.handleRotate)
	mux.HandleFunc("POST /v1/agents/{nick}/revoke", s.handleRevoke)

	return s.authMiddleware(mux)
}
