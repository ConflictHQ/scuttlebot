// Package api implements the scuttlebot HTTP management API.
//
// /v1/ endpoints require a valid Bearer token.
// /ui/ is served unauthenticated (static web UI).
// /v1/channels/{channel}/stream uses ?token= query param (EventSource limitation).
package api

import (
	"log/slog"
	"net/http"

	"github.com/conflicthq/scuttlebot/internal/auth"
	"github.com/conflicthq/scuttlebot/internal/config"
	"github.com/conflicthq/scuttlebot/internal/registry"
)

// ircPasswdSetter can change an IRC (NickServ) account's password.
type ircPasswdSetter interface {
	ChangePassword(name, passphrase string) error
}

// Server is the scuttlebot HTTP API server.
type Server struct {
	registry   *registry.Registry
	apiKeys    *auth.APIKeyStore
	log        *slog.Logger
	bridge     chatBridge        // nil if bridge is disabled
	policies   *PolicyStore      // nil if not configured
	admins     adminStore        // nil if not configured
	llmCfg     *config.LLMConfig // nil if no LLM backends configured
	topoMgr    topologyManager   // nil if topology not configured
	cfgStore   *ConfigStore      // nil if config write-back not configured
	ircPasswd  ircPasswdSetter   // nil if not configured
	loginRL    *loginRateLimiter
	tlsDomain  string // empty if no TLS
	noAuthMode bool   // SCUTTLEBOT_NO_AUTH: UI auto-logs in without credentials
	showToken  bool   // SCUTTLEBOT_SHOW_TOKEN: UI shows a dev token in the login modal
}

// New creates a new API Server. Pass nil for b to disable the chat bridge.
// Pass nil for admins to disable admin authentication endpoints.
// Pass nil for llmCfg to disable AI/LLM management endpoints.
// Pass nil for topo to disable topology provisioning endpoints.
// Pass nil for cfgStore to disable config read/write endpoints.
// Pass nil for ircPasswd to disable IRC user password management.
// noAuthMode and showToken are mutually exclusive trusted-environment modes
// (set via SCUTTLEBOT_NO_AUTH / SCUTTLEBOT_SHOW_TOKEN).
func New(reg *registry.Registry, apiKeys *auth.APIKeyStore, b chatBridge, ps *PolicyStore, admins adminStore, llmCfg *config.LLMConfig, topo topologyManager, cfgStore *ConfigStore, ircPasswd ircPasswdSetter, tlsDomain string, noAuthMode, showToken bool, log *slog.Logger) *Server {
	return &Server{
		registry:   reg,
		apiKeys:    apiKeys,
		log:        log,
		bridge:     b,
		policies:   ps,
		admins:     admins,
		llmCfg:     llmCfg,
		topoMgr:    topo,
		cfgStore:   cfgStore,
		ircPasswd:  ircPasswd,
		loginRL:    newLoginRateLimiter(),
		tlsDomain:  tlsDomain,
		noAuthMode: noAuthMode,
		showToken:  showToken,
	}
}

// Handler returns the HTTP handler with all routes registered.
// /v1/ routes require a valid Bearer token. /ui/ is served unauthenticated.
// Scoped routes additionally check the API key's scopes.
func (s *Server) Handler() http.Handler {
	apiMux := http.NewServeMux()

	// Read-scope: status, metrics (also accessible with any scope via admin).
	apiMux.HandleFunc("GET /v1/status", s.requireScope(auth.ScopeRead, s.handleStatus))
	apiMux.HandleFunc("GET /v1/metrics", s.requireScope(auth.ScopeRead, s.handleMetrics))

	// Policies — admin scope.
	if s.policies != nil {
		apiMux.HandleFunc("GET /v1/settings", s.requireScope(auth.ScopeRead, s.handleGetSettings))
		apiMux.HandleFunc("GET /v1/settings/policies", s.requireScope(auth.ScopeRead, s.handleGetPolicies))
		apiMux.HandleFunc("PUT /v1/settings/policies", s.requireScope(auth.ScopeAdmin, s.handlePutPolicies))
		apiMux.HandleFunc("PATCH /v1/settings/policies", s.requireScope(auth.ScopeAdmin, s.handlePatchPolicies))
	}

	// Agents — agents scope.
	apiMux.HandleFunc("GET /v1/agents", s.requireScope(auth.ScopeAgents, s.handleListAgents))
	apiMux.HandleFunc("GET /v1/agents/{nick}", s.requireScope(auth.ScopeAgents, s.handleGetAgent))
	apiMux.HandleFunc("PATCH /v1/agents/{nick}", s.requireScope(auth.ScopeAgents, s.handleUpdateAgent))
	apiMux.HandleFunc("POST /v1/agents/register", s.requireScope(auth.ScopeAgents, s.handleRegister))
	apiMux.HandleFunc("POST /v1/agents/{nick}/rotate", s.requireScope(auth.ScopeAgents, s.handleRotate))
	apiMux.HandleFunc("POST /v1/agents/{nick}/adopt", s.requireScope(auth.ScopeAgents, s.handleAdopt))
	apiMux.HandleFunc("POST /v1/agents/{nick}/revoke", s.requireScope(auth.ScopeAgents, s.handleRevoke))
	apiMux.HandleFunc("DELETE /v1/agents/{nick}", s.requireScope(auth.ScopeAgents, s.handleDelete))
	apiMux.HandleFunc("POST /v1/agents/bulk-delete", s.requireScope(auth.ScopeAgents, s.handleBulkDeleteAgents))

	// Channels — channels scope (read), chat scope (send).
	if s.bridge != nil {
		apiMux.HandleFunc("GET /v1/channels", s.requireScope(auth.ScopeChannels, s.handleListChannels))
		apiMux.HandleFunc("POST /v1/channels/{channel}/join", s.requireScope(auth.ScopeChannels, s.handleJoinChannel))
		apiMux.HandleFunc("DELETE /v1/channels/{channel}", s.requireScope(auth.ScopeChannels, s.handleDeleteChannel))
		apiMux.HandleFunc("GET /v1/channels/{channel}/messages", s.requireScope(auth.ScopeChannels, s.handleChannelMessages))
		apiMux.HandleFunc("POST /v1/channels/{channel}/messages", s.requireScope(auth.ScopeChat, s.handleSendMessage))
		apiMux.HandleFunc("POST /v1/channels/{channel}/presence", s.requireScope(auth.ScopeChat, s.handleChannelPresence))
		apiMux.HandleFunc("GET /v1/channels/{channel}/users", s.requireScope(auth.ScopeChannels, s.handleChannelUsers))
		apiMux.HandleFunc("GET /v1/channels/{channel}/config", s.requireScope(auth.ScopeChannels, s.handleGetChannelConfig))
		apiMux.HandleFunc("PUT /v1/channels/{channel}/config", s.requireScope(auth.ScopeAdmin, s.handlePutChannelConfig))
	}

	// Topology — topology scope.
	if s.topoMgr != nil {
		apiMux.HandleFunc("POST /v1/channels", s.requireScope(auth.ScopeTopology, s.handleProvisionChannel))
		apiMux.HandleFunc("DELETE /v1/topology/channels/{channel}", s.requireScope(auth.ScopeTopology, s.handleDropChannel))
		apiMux.HandleFunc("GET /v1/topology", s.requireScope(auth.ScopeTopology, s.handleGetTopology))
	}
	// Blocker escalation — agents can signal they're stuck.
	if s.bridge != nil {
		apiMux.HandleFunc("POST /v1/agents/{nick}/blocker", s.requireScope(auth.ScopeAgents, s.handleAgentBlocker))
	}

	// Instructions — available even without topology (uses policies store).
	apiMux.HandleFunc("GET /v1/channels/{channel}/instructions", s.requireScope(auth.ScopeTopology, s.handleGetInstructions))
	apiMux.HandleFunc("PUT /v1/channels/{channel}/instructions", s.requireScope(auth.ScopeTopology, s.handlePutInstructions))
	apiMux.HandleFunc("DELETE /v1/channels/{channel}/instructions", s.requireScope(auth.ScopeTopology, s.handleDeleteInstructions))

	// Config — config scope.
	if s.cfgStore != nil {
		apiMux.HandleFunc("GET /v1/config", s.requireScope(auth.ScopeConfig, s.handleGetConfig))
		apiMux.HandleFunc("PUT /v1/config", s.requireScope(auth.ScopeConfig, s.handlePutConfig))
		apiMux.HandleFunc("GET /v1/config/history", s.requireScope(auth.ScopeConfig, s.handleGetConfigHistory))
		apiMux.HandleFunc("GET /v1/config/history/{filename}", s.requireScope(auth.ScopeConfig, s.handleGetConfigHistoryEntry))
	}

	// Admin — admin scope.
	if s.admins != nil {
		apiMux.HandleFunc("GET /v1/admins", s.requireScope(auth.ScopeAdmin, s.handleAdminList))
		apiMux.HandleFunc("POST /v1/admins", s.requireScope(auth.ScopeAdmin, s.handleAdminAdd))
		apiMux.HandleFunc("DELETE /v1/admins/{username}", s.requireScope(auth.ScopeAdmin, s.handleAdminRemove))
		apiMux.HandleFunc("PUT /v1/admins/{username}/password", s.requireScope(auth.ScopeAdmin, s.handleAdminSetPassword))
	}

	// API key management — admin scope.
	apiMux.HandleFunc("GET /v1/api-keys", s.requireScope(auth.ScopeAdmin, s.handleListAPIKeys))
	apiMux.HandleFunc("POST /v1/api-keys", s.requireScope(auth.ScopeAdmin, s.handleCreateAPIKey))
	apiMux.HandleFunc("PUT /v1/api-keys/{id}/rotate", s.requireScope(auth.ScopeAdmin, s.handleRotateAPIKey))
	apiMux.HandleFunc("DELETE /v1/api-keys/{id}", s.requireScope(auth.ScopeAdmin, s.handleRevokeAPIKey))

	// IRC user management — admin scope.
	if s.ircPasswd != nil {
		apiMux.HandleFunc("PUT /v1/users/{nick}/password", s.requireScope(auth.ScopeAdmin, s.handleUserSetPassword))
	}

	// LLM / AI gateway — bots scope.
	apiMux.HandleFunc("GET /v1/llm/backends", s.requireScope(auth.ScopeBots, s.handleLLMBackends))
	apiMux.HandleFunc("POST /v1/llm/backends", s.requireScope(auth.ScopeBots, s.handleLLMBackendCreate))
	apiMux.HandleFunc("PUT /v1/llm/backends/{name}", s.requireScope(auth.ScopeBots, s.handleLLMBackendUpdate))
	apiMux.HandleFunc("DELETE /v1/llm/backends/{name}", s.requireScope(auth.ScopeBots, s.handleLLMBackendDelete))
	apiMux.HandleFunc("GET /v1/llm/backends/{name}/models", s.requireScope(auth.ScopeBots, s.handleLLMModels))
	apiMux.HandleFunc("POST /v1/llm/discover", s.requireScope(auth.ScopeBots, s.handleLLMDiscover))
	apiMux.HandleFunc("GET /v1/llm/known", s.requireScope(auth.ScopeBots, s.handleLLMKnown))
	apiMux.HandleFunc("POST /v1/llm/complete", s.requireScope(auth.ScopeBots, s.handleLLMComplete))

	outer := http.NewServeMux()
	outer.HandleFunc("POST /login", s.handleLogin)
	outer.HandleFunc("GET /dev-token", s.handleDevToken)
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
