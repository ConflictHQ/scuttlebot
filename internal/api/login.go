package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/conflicthq/scuttlebot/internal/auth"
)

// adminStore is the interface the Server uses for admin operations.
type adminStore interface {
	Authenticate(username, password string) bool
	List() []auth.Admin
	Add(username, password string) error
	Remove(username string) error
	SetPassword(username, password string) error
}

// loginRateLimiter enforces a per-IP sliding window of 10 attempts per minute.
type loginRateLimiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time
}

func newLoginRateLimiter() *loginRateLimiter {
	return &loginRateLimiter{windows: make(map[string][]time.Time)}
}

// Allow returns true if the IP is within the allowed rate.
func (rl *loginRateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	const (
		maxAttempts = 10
		window      = time.Minute
	)

	now := time.Now()
	cutoff := now.Add(-window)

	prev := rl.windows[ip]
	var kept []time.Time
	for _, t := range prev {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	rl.windows[ip] = kept

	return len(kept) <= maxAttempts
}

func clientIP(r *http.Request) string {
	// Use RemoteAddr; X-Forwarded-For is not trustworthy without proxy config.
	host := r.RemoteAddr
	// Strip port if present.
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			host = host[:i]
			break
		}
	}
	return host
}

// handleLogin handles POST /login.
// Unauthenticated. Returns {token, username} on success.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.admins == nil {
		writeError(w, http.StatusNotFound, "admin authentication not configured")
		return
	}

	ip := clientIP(r)
	if !s.loginRL.Allow(ip) {
		writeError(w, http.StatusTooManyRequests, "too many login attempts")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !s.admins.Authenticate(req.Username, req.Password) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	// Create a session API key for this admin login.
	sessionName := "session:" + req.Username
	token, _, err := s.apiKeys.Create(sessionName, []auth.Scope{auth.ScopeAdmin}, time.Now().Add(24*time.Hour), "")
	if err != nil {
		s.log.Error("login: create session key", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"token":    token,
		"username": req.Username,
	})
}

// handleAdminList handles GET /v1/admins.
func (s *Server) handleAdminList(w http.ResponseWriter, r *http.Request) {
	admins := s.admins.List()
	type adminView struct {
		Username string    `json:"username"`
		Created  time.Time `json:"created"`
	}
	out := make([]adminView, len(admins))
	for i, a := range admins {
		out[i] = adminView{Username: a.Username, Created: a.Created}
	}
	writeJSON(w, http.StatusOK, map[string]any{"admins": out})
}

// handleAdminAdd handles POST /v1/admins.
func (s *Server) handleAdminAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}

	if err := s.admins.Add(req.Username, req.Password); err != nil {
		// Add returns an error if username already exists.
		writeError(w, http.StatusConflict, "username already exists")
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// handleAdminRemove handles DELETE /v1/admins/{username}.
func (s *Server) handleAdminRemove(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	if err := s.admins.Remove(username); err != nil {
		writeError(w, http.StatusNotFound, "admin not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAdminSetPassword handles PUT /v1/admins/{username}/password.
func (s *Server) handleAdminSetPassword(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Password == "" {
		writeError(w, http.StatusBadRequest, "password required")
		return
	}

	if err := s.admins.SetPassword(username, req.Password); err != nil {
		writeError(w, http.StatusNotFound, "admin not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
