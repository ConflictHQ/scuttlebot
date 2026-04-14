package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// Scope represents a permission scope for an API key.
type Scope string

const (
	ScopeAdmin    Scope = "admin"    // full access
	ScopeAgents   Scope = "agents"   // agent registration, rotation, revocation
	ScopeChannels Scope = "channels" // channel CRUD, join, messages, presence
	ScopeTopology Scope = "topology" // channel provisioning, topology management
	ScopeBots     Scope = "bots"     // bot configuration, start/stop
	ScopeConfig   Scope = "config"   // server config read/write
	ScopeRead     Scope = "read"     // read-only access to all GET endpoints
	ScopeChat     Scope = "chat"     // send/receive messages only
)

// ValidScopes is the set of all recognised scopes.
var ValidScopes = map[Scope]bool{
	ScopeAdmin: true, ScopeAgents: true, ScopeChannels: true,
	ScopeTopology: true, ScopeBots: true, ScopeConfig: true,
	ScopeRead: true, ScopeChat: true,
}

// APIKey is a single API key record.
type APIKey struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Hash      string    `json:"hash"` // SHA-256 of the plaintext token
	Scopes    []Scope   `json:"scopes"`
	Team      string    `json:"team,omitempty"` // empty = unrestricted; non-empty = scoped to this team
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"` // zero = never
	Active    bool      `json:"active"`
}

// HasScope reports whether the key has the given scope (or admin, which implies all).
func (k *APIKey) HasScope(s Scope) bool {
	for _, scope := range k.Scopes {
		if scope == ScopeAdmin || scope == s {
			return true
		}
	}
	return false
}

// IsExpired reports whether the key has passed its expiry time.
func (k *APIKey) IsExpired() bool {
	return !k.ExpiresAt.IsZero() && time.Now().After(k.ExpiresAt)
}

// APIKeyStore persists API keys to a JSON file.
type APIKeyStore struct {
	mu   sync.RWMutex
	path string
	data []APIKey
}

// NewAPIKeyStore loads (or creates) the API key store at the given path.
func NewAPIKeyStore(path string) (*APIKeyStore, error) {
	s := &APIKeyStore{path: path}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Create generates a new API key with the given name, scopes, and optional team scope.
// Returns the plaintext token (shown only once) and the stored key record.
func (s *APIKeyStore) Create(name string, scopes []Scope, expiresAt time.Time, team string) (plaintext string, key APIKey, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	token, err := genToken()
	if err != nil {
		return "", APIKey{}, fmt.Errorf("apikeys: generate token: %w", err)
	}

	key = APIKey{
		ID:        newULID(),
		Name:      name,
		Hash:      hashToken(token),
		Scopes:    scopes,
		Team:      team,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: expiresAt,
		Active:    true,
	}
	s.data = append(s.data, key)
	if err := s.save(); err != nil {
		// Roll back.
		s.data = s.data[:len(s.data)-1]
		return "", APIKey{}, err
	}
	return token, key, nil
}

// Insert adds a pre-built API key with a known plaintext token.
// Used for migrating the startup token into the store.
// Inserted keys have no team scope (unrestricted).
func (s *APIKeyStore) Insert(name, plaintext string, scopes []Scope) (APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := APIKey{
		ID:        newULID(),
		Name:      name,
		Hash:      hashToken(plaintext),
		Scopes:    scopes,
		CreatedAt: time.Now().UTC(),
		Active:    true,
	}
	s.data = append(s.data, key)
	if err := s.save(); err != nil {
		s.data = s.data[:len(s.data)-1]
		return APIKey{}, err
	}
	return key, nil
}

// Lookup finds an active, non-expired key by plaintext token.
// Returns nil if no match.
func (s *APIKeyStore) Lookup(token string) *APIKey {
	hash := hashToken(token)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.data {
		if s.data[i].Hash == hash && s.data[i].Active && !s.data[i].IsExpired() {
			k := s.data[i]
			return &k
		}
	}
	return nil
}

// TouchLastUsed updates the last-used timestamp for a key by ID.
func (s *APIKeyStore) TouchLastUsed(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data {
		if s.data[i].ID == id {
			s.data[i].LastUsed = time.Now().UTC()
			_ = s.save() // best-effort persistence
			return
		}
	}
}

// Get returns a key by ID, or nil if not found.
func (s *APIKeyStore) Get(id string) *APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.data {
		if s.data[i].ID == id {
			k := s.data[i]
			return &k
		}
	}
	return nil
}

// List returns all keys (active and revoked).
func (s *APIKeyStore) List() []APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]APIKey, len(s.data))
	copy(out, s.data)
	return out
}

// Rotate generates a new token for an existing key by ID.
// The old token is immediately invalidated. Returns the new plaintext token.
func (s *APIKeyStore) Rotate(id string) (plaintext string, key APIKey, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data {
		if s.data[i].ID == id {
			token, err := genToken()
			if err != nil {
				return "", APIKey{}, fmt.Errorf("apikeys: generate token: %w", err)
			}
			s.data[i].Hash = hashToken(token)
			if err := s.save(); err != nil {
				return "", APIKey{}, fmt.Errorf("apikeys: save after rotate: %w", err)
			}
			return token, s.data[i], nil
		}
	}
	return "", APIKey{}, fmt.Errorf("apikeys: key %q not found", id)
}

// Revoke deactivates a key by ID.
func (s *APIKeyStore) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data {
		if s.data[i].ID == id {
			if !s.data[i].Active {
				return fmt.Errorf("apikeys: key %q already revoked", id)
			}
			s.data[i].Active = false
			return s.save()
		}
	}
	return fmt.Errorf("apikeys: key %q not found", id)
}

// Lookup (TokenValidator interface) reports whether the token is valid.
// Satisfies the mcp.TokenValidator interface.
func (s *APIKeyStore) ValidToken(token string) bool {
	return s.Lookup(token) != nil
}

// TestStore creates an in-memory APIKeyStore with a single admin-scope key
// for the given token. Intended for tests only — does not persist to disk.
func TestStore(token string) *APIKeyStore {
	s := &APIKeyStore{path: "", data: []APIKey{{
		ID:        "test-key",
		Name:      "test",
		Hash:      hashToken(token),
		Scopes:    []Scope{ScopeAdmin},
		CreatedAt: time.Now().UTC(),
		Active:    true,
	}}}
	return s
}

// TestStoreWithTeam creates an in-memory APIKeyStore with two keys: an
// admin-scope key for adminToken (unrestricted) and a team-scoped key for
// teamToken with the given scopes and team. Intended for tests only.
func TestStoreWithTeam(adminToken, teamToken string, scopes []Scope, team string) *APIKeyStore {
	s := &APIKeyStore{path: "", data: []APIKey{
		{
			ID:        "admin-key",
			Name:      "admin",
			Hash:      hashToken(adminToken),
			Scopes:    []Scope{ScopeAdmin},
			CreatedAt: time.Now().UTC(),
			Active:    true,
		},
		{
			ID:        "team-key",
			Name:      "team-" + team,
			Hash:      hashToken(teamToken),
			Scopes:    scopes,
			Team:      team,
			CreatedAt: time.Now().UTC(),
			Active:    true,
		},
	}}
	return s
}

// IsEmpty reports whether there are no keys.
func (s *APIKeyStore) IsEmpty() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data) == 0
}

func (s *APIKeyStore) load() error {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("apikeys: read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return fmt.Errorf("apikeys: parse: %w", err)
	}
	return nil
}

func (s *APIKeyStore) save() error {
	if s.path == "" {
		return nil // in-memory only (tests)
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0600)
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func genToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func newULID() string {
	entropy := ulid.Monotonic(rand.Reader, 0)
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
}

// ParseScopes parses a comma-separated scope string into a slice.
// Returns an error if any scope is unrecognised.
func ParseScopes(s string) ([]Scope, error) {
	parts := strings.Split(s, ",")
	scopes := make([]Scope, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		scope := Scope(p)
		if !ValidScopes[scope] {
			return nil, fmt.Errorf("unknown scope %q", p)
		}
		scopes = append(scopes, scope)
	}
	if len(scopes) == 0 {
		return nil, fmt.Errorf("at least one scope is required")
	}
	return scopes, nil
}
