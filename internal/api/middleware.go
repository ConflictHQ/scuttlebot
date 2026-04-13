package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/conflicthq/scuttlebot/internal/auth"
)

type ctxKey string

const ctxAPIKey ctxKey = "apikey"

// apiKeyFromContext returns the authenticated APIKey from the request context,
// or nil if not authenticated.
func apiKeyFromContext(ctx context.Context) *auth.APIKey {
	k, _ := ctx.Value(ctxAPIKey).(*auth.APIKey)
	return k
}

// authMiddleware validates the Bearer token and injects the APIKey into context.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing authorization header")
			return
		}
		key := s.apiKeys.Lookup(token)
		if key == nil {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		// Update last-used timestamp in the background.
		go s.apiKeys.TouchLastUsed(key.ID)

		ctx := context.WithValue(r.Context(), ctxAPIKey, key)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireScope returns middleware that rejects requests without the given scope.
func (s *Server) requireScope(scope auth.Scope, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := apiKeyFromContext(r.Context())
		if key == nil {
			writeError(w, http.StatusUnauthorized, "missing authentication")
			return
		}
		if !key.HasScope(scope) {
			writeError(w, http.StatusForbidden, "insufficient scope: requires "+string(scope))
			return
		}
		next(w, r)
	}
}

// teamFromRequest returns the team scope of the authenticated API key,
// or "" if the key is unrestricted (no team) or unauthenticated.
func teamFromRequest(r *http.Request) string {
	key := apiKeyFromContext(r.Context())
	if key == nil {
		return ""
	}
	return key.Team
}

func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	token, found := strings.CutPrefix(auth, "Bearer ")
	if !found {
		return ""
	}
	return strings.TrimSpace(token)
}
