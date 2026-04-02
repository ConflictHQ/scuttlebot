// Package api implements the scuttlebot HTTP management API.
//
// /v1/ endpoints require a valid Bearer token.
// /ui/ is served unauthenticated (static web UI).
// /v1/channels/{channel}/stream uses ?token= query param (EventSource limitation).
package api

import (
	"log/slog"
	"net/http"

	"github.com/conflicthq/scuttlebot/internal/config"
	"github.com/conflicthq/scuttlebot/internal/registry"
)

// Server is the scuttlebot HTTP API server.
type Server struct {
	registry  *registry.Registry
	tokens    map[string]struct{}
	log       *slog.Logger
	bridge    chatBridge        // nil if bridge is disabled
	policies  *PolicyStore      // nil if not configured
	admins    adminStore        // nil if not configured
	llmCfg    *config.LLMConfig // nil if no LLM backends configured
	topoMgr   topologyManager   // nil if topology not configured
	cfgStore  *ConfigStore      // nil if config write-back not configured
	loginRL   *loginRateLimiter
	tlsDomain string // empty if no TLS
}

// New creates a new API Server. Pass nil for b to disable the chat bridge.
// Pass nil for admins to disable admin authentication endpoints.
// Pass nil for llmCfg to disable AI/LLM management endpoints.
// Pass nil for topo to disable topology provisioning endpoints.
// Pass nil for cfgStore to disable config read/write endpoints.
func New(reg *registry.Registry, tokens []string, b chatBridge, ps *PolicyStore, admins adminStore, llmCfg *config.LLMConfig, topo topologyManager, cfgStore *ConfigStore, tlsDomain string, log *slog.Logger) *Server {
	tokenSet := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		tokenSet[t] = struct{}{}
	}
	return &Server{
		registry:  reg,
		tokens:    tokenSet,
		log:       log,
		bridge:    b,
		policies:  ps,
		admins:    admins,
		llmCfg:    llmCfg,
		topoMgr:   topo,
		cfgStore:  cfgStore,
		loginRL:   newLoginRateLimiter(),
		tlsDomain: tlsDomain,
	}
}

// Handler returns the HTTP handler with all routes registered.
// /v1/ routes require a valid Bearer token. /ui/ is served unauthenticated.
func (s *Server) Handler() http.Handler {
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /v1/status", s.handleStatus)
	apiMux.HandleFunc("GET /v1/metrics", s.handleMetrics)
	if s.policies != nil {
		apiMux.HandleFunc("GET /v1/settings", s.handleGetSettings)
		apiMux.HandleFunc("GET /v1/settings/policies", s.handleGetPolicies)
		apiMux.HandleFunc("PUT /v1/settings/policies", s.handlePutPolicies)
	}
	apiMux.HandleFunc("GET /v1/agents", s.handleListAgents)
	apiMux.HandleFunc("GET /v1/agents/{nick}", s.handleGetAgent)
	apiMux.HandleFunc("PATCH /v1/agents/{nick}", s.handleUpdateAgent)
	apiMux.HandleFunc("POST /v1/agents/register", s.handleRegister)
	apiMux.HandleFunc("POST /v1/agents/{nick}/rotate", s.handleRotate)
	apiMux.HandleFunc("POST /v1/agents/{nick}/adopt", s.handleAdopt)
	apiMux.HandleFunc("POST /v1/agents/{nick}/revoke", s.handleRevoke)
	apiMux.HandleFunc("DELETE /v1/agents/{nick}", s.handleDelete)
	if s.bridge != nil {
		apiMux.HandleFunc("GET /v1/channels", s.handleListChannels)
		apiMux.HandleFunc("POST /v1/channels/{channel}/join", s.handleJoinChannel)
		apiMux.HandleFunc("DELETE /v1/channels/{channel}", s.handleDeleteChannel)
		apiMux.HandleFunc("GET /v1/channels/{channel}/messages", s.handleChannelMessages)
		apiMux.HandleFunc("POST /v1/channels/{channel}/messages", s.handleSendMessage)
		apiMux.HandleFunc("POST /v1/channels/{channel}/presence", s.handleChannelPresence)
		apiMux.HandleFunc("GET /v1/channels/{channel}/users", s.handleChannelUsers)
	}
	if s.topoMgr != nil {
		apiMux.HandleFunc("POST /v1/channels", s.handleProvisionChannel)
		apiMux.HandleFunc("DELETE /v1/topology/channels/{channel}", s.handleDropChannel)
		apiMux.HandleFunc("GET /v1/topology", s.handleGetTopology)
	}
	if s.cfgStore != nil {
		apiMux.HandleFunc("GET /v1/config", s.handleGetConfig)
		apiMux.HandleFunc("PUT /v1/config", s.handlePutConfig)
		apiMux.HandleFunc("GET /v1/config/history", s.handleGetConfigHistory)
		apiMux.HandleFunc("GET /v1/config/history/{filename}", s.handleGetConfigHistoryEntry)
	}

	if s.admins != nil {
		apiMux.HandleFunc("GET /v1/admins", s.handleAdminList)
		apiMux.HandleFunc("POST /v1/admins", s.handleAdminAdd)
		apiMux.HandleFunc("DELETE /v1/admins/{username}", s.handleAdminRemove)
		apiMux.HandleFunc("PUT /v1/admins/{username}/password", s.handleAdminSetPassword)
	}

	// LLM / AI gateway endpoints.
	apiMux.HandleFunc("GET /v1/llm/backends", s.handleLLMBackends)
	apiMux.HandleFunc("POST /v1/llm/backends", s.handleLLMBackendCreate)
	apiMux.HandleFunc("PUT /v1/llm/backends/{name}", s.handleLLMBackendUpdate)
	apiMux.HandleFunc("DELETE /v1/llm/backends/{name}", s.handleLLMBackendDelete)
	apiMux.HandleFunc("GET /v1/llm/backends/{name}/models", s.handleLLMModels)
	apiMux.HandleFunc("POST /v1/llm/discover", s.handleLLMDiscover)
	apiMux.HandleFunc("GET /v1/llm/known", s.handleLLMKnown)
	apiMux.HandleFunc("POST /v1/llm/complete", s.handleLLMComplete)

	outer := http.NewServeMux()
	outer.HandleFunc("POST /login", s.handleLogin)
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
