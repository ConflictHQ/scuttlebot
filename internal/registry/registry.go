// Package registry manages agent registration and credential lifecycle.
//
// Agents register with scuttlebot and receive SASL credentials for the Ergo
// IRC server, plus a signed rules-of-engagement payload describing their
// channel assignments and permissions.
package registry

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/conflicthq/scuttlebot/internal/store"
)

// AgentType describes an agent's role and authority level.
type AgentType string

const (
	AgentTypeOperator     AgentType = "operator"     // human operator — +o + full permissions
	AgentTypeOrchestrator AgentType = "orchestrator" // +o in channels
	AgentTypeWorker       AgentType = "worker"       // +v in channels
	AgentTypeObserver     AgentType = "observer"     // no special mode
)

// Agent is a registered agent.
type Agent struct {
	Nick        string           `json:"nick"`
	Type        AgentType        `json:"type"`
	Channels    []string         `json:"channels"`    // convenience: same as Config.Channels
	Permissions []string         `json:"permissions"` // convenience: same as Config.Permissions
	Config      EngagementConfig `json:"config"`
	CreatedAt   time.Time        `json:"created_at"`
	Revoked     bool             `json:"revoked"`
	LastSeen    *time.Time       `json:"last_seen,omitempty"`
	Online      bool             `json:"online"`
}

// Credentials are the SASL credentials an agent uses to connect to Ergo.
type Credentials struct {
	Nick       string `json:"nick"`
	Passphrase string `json:"passphrase"`
}

// EngagementPayload is the signed payload delivered to an agent on registration.
// Agents verify this with VerifyPayload() before trusting its contents.
type EngagementPayload struct {
	V        int              `json:"v"`
	Nick     string           `json:"nick"`
	Type     AgentType        `json:"type"`
	Config   EngagementConfig `json:"config"`
	IssuedAt time.Time        `json:"issued_at"`
}

// SignedPayload wraps an EngagementPayload with an HMAC signature.
type SignedPayload struct {
	Payload   EngagementPayload `json:"payload"`
	Signature string            `json:"signature"` // hex-encoded HMAC-SHA256
}

// AccountProvisioner is the interface the registry uses to create/modify IRC accounts.
// Implemented by *ergo.APIClient in production; can be mocked in tests.
type AccountProvisioner interface {
	RegisterAccount(name, passphrase string) error
	ChangePassword(name, passphrase string) error
}

// Registry manages registered agents and their credentials.
type Registry struct {
	mu            sync.RWMutex
	agents        map[string]*Agent // keyed by nick
	provisioner   AccountProvisioner
	signingKey    []byte
	dataPath      string       // path to persist agents JSON; empty = no persistence
	db            *store.Store // when non-nil, supersedes dataPath
	onlineTimeout time.Duration
}

// New creates a new Registry with the given provisioner and HMAC signing key.
// Call SetDataPath to enable persistence before registering any agents.
func New(provisioner AccountProvisioner, signingKey []byte) *Registry {
	return &Registry{
		agents:      make(map[string]*Agent),
		provisioner: provisioner,
		signingKey:  signingKey,
	}
}

// SetDataPath enables file-based persistence. The registry is loaded from path
// immediately (non-fatal if the file doesn't exist yet) and saved there after
// every mutation. Mutually exclusive with SetStore.
func (r *Registry) SetDataPath(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dataPath = path
	return r.load()
}

// SetStore switches the registry to database-backed persistence. All current
// in-memory state is replaced with rows loaded from the store. Mutually
// exclusive with SetDataPath.
func (r *Registry) SetStore(db *store.Store) error {
	rows, err := db.AgentList()
	if err != nil {
		return fmt.Errorf("registry: load from store: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.db = db
	r.dataPath = "" // DB takes over
	r.agents = make(map[string]*Agent, len(rows))
	for _, row := range rows {
		var cfg EngagementConfig
		if err := json.Unmarshal(row.Config, &cfg); err != nil {
			return fmt.Errorf("registry: decode agent %s config: %w", row.Nick, err)
		}
		a := &Agent{
			Nick:        row.Nick,
			Type:        AgentType(row.Type),
			Channels:    cfg.Channels,
			Permissions: cfg.Permissions,
			Config:      cfg,
			CreatedAt:   row.CreatedAt,
			Revoked:     row.Revoked,
			LastSeen:    row.LastSeen,
		}
		r.agents[a.Nick] = a
	}
	return nil
}

// saveOne persists a single agent. Uses the DB when available, otherwise
// falls back to a full file rewrite.
func (r *Registry) saveOne(a *Agent) {
	if r.db != nil {
		cfg, _ := json.Marshal(a.Config)
		_ = r.db.AgentUpsert(&store.AgentRow{
			Nick:      a.Nick,
			Type:      string(a.Type),
			Config:    cfg,
			CreatedAt: a.CreatedAt,
			Revoked:   a.Revoked,
			LastSeen:  a.LastSeen,
		})
		return
	}
	r.save()
}

// deleteOne removes a single agent from the store. Uses the DB when available,
// otherwise falls back to a full file rewrite (agent already removed from map).
func (r *Registry) deleteOne(nick string) {
	if r.db != nil {
		_ = r.db.AgentDelete(nick)
		return
	}
	r.save()
}

func (r *Registry) load() error {
	data, err := os.ReadFile(r.dataPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("registry: load: %w", err)
	}
	var agents []*Agent
	if err := json.Unmarshal(data, &agents); err != nil {
		return fmt.Errorf("registry: load: %w", err)
	}
	for _, a := range agents {
		r.agents[a.Nick] = a
	}
	return nil
}

func (r *Registry) save() {
	if r.dataPath == "" {
		return
	}
	agents := make([]*Agent, 0, len(r.agents))
	for _, a := range r.agents {
		agents = append(agents, a)
	}
	data, err := json.MarshalIndent(agents, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(r.dataPath, data, 0600)
}

// Register creates a new agent, provisions its Ergo account, and returns
// credentials and a signed rules-of-engagement payload.
// cfg is validated before any provisioning occurs.
func (r *Registry) Register(nick string, agentType AgentType, cfg EngagementConfig) (*Credentials, *SignedPayload, error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("registry: invalid engagement config: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.agents[nick]; ok && !existing.Revoked {
		return nil, nil, fmt.Errorf("registry: agent %q already registered", nick)
	}

	passphrase, err := generatePassphrase()
	if err != nil {
		return nil, nil, fmt.Errorf("registry: generate passphrase: %w", err)
	}

	if err := r.provisioner.RegisterAccount(nick, passphrase); err != nil {
		// Account exists in NickServ from a previous run — sync the password.
		if strings.Contains(err.Error(), "ACCOUNT_EXISTS") {
			if err2 := r.provisioner.ChangePassword(nick, passphrase); err2 != nil {
				return nil, nil, fmt.Errorf("registry: provision account: %w", err2)
			}
		} else {
			return nil, nil, fmt.Errorf("registry: provision account: %w", err)
		}
	}

	agent := &Agent{
		Nick:        nick,
		Type:        agentType,
		Channels:    cfg.Channels,
		Permissions: cfg.Permissions,
		Config:      cfg,
		CreatedAt:   time.Now(),
	}
	r.agents[nick] = agent
	r.saveOne(agent)

	payload, err := r.signPayload(agent)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: sign payload: %w", err)
	}

	return &Credentials{Nick: nick, Passphrase: passphrase}, payload, nil
}

// Adopt adds a pre-existing NickServ account to the registry without touching
// its password. The caller is responsible for knowing their own passphrase.
// Returns a signed payload; no Credentials are returned since the password
// is not changed.
func (r *Registry) Adopt(nick string, agentType AgentType, cfg EngagementConfig) (*SignedPayload, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("registry: invalid engagement config: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.agents[nick]; ok && !existing.Revoked {
		return nil, fmt.Errorf("registry: agent %q already registered", nick)
	}

	agent := &Agent{
		Nick:        nick,
		Type:        agentType,
		Channels:    cfg.Channels,
		Permissions: cfg.Permissions,
		Config:      cfg,
		CreatedAt:   time.Now(),
	}
	r.agents[nick] = agent
	r.saveOne(agent)

	return r.signPayload(agent)
}

// Rotate generates a new passphrase for an agent and updates Ergo.
func (r *Registry) Rotate(nick string) (*Credentials, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, err := r.get(nick); err != nil {
		return nil, err
	}

	passphrase, err := generatePassphrase()
	if err != nil {
		return nil, fmt.Errorf("registry: generate passphrase: %w", err)
	}

	if err := r.provisioner.ChangePassword(nick, passphrase); err != nil {
		return nil, fmt.Errorf("registry: rotate credentials: %w", err)
	}

	// Rotation doesn't change stored agent data, but bump a file save for
	// consistency; DB backends are unaffected since nothing persisted changed.
	r.save()
	return &Credentials{Nick: nick, Passphrase: passphrase}, nil
}

// Revoke locks an agent out by rotating to an unguessable passphrase and
// marking it revoked in the registry.
func (r *Registry) Revoke(nick string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	agent, err := r.get(nick)
	if err != nil {
		return err
	}

	lockout, err := generatePassphrase()
	if err != nil {
		return fmt.Errorf("registry: generate lockout passphrase: %w", err)
	}

	if err := r.provisioner.ChangePassword(nick, lockout); err != nil {
		return fmt.Errorf("registry: revoke credentials: %w", err)
	}

	agent.Revoked = true
	r.saveOne(agent)
	return nil
}

// Delete fully removes an agent from the registry. The Ergo NickServ account
// is locked out first (password rotated to an unguessable value) so the agent
// can no longer connect, then the entry is removed from the registry. If the
// agent is already revoked the lockout step is skipped.
func (r *Registry) Delete(nick string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	agent, ok := r.agents[nick]
	if !ok {
		return fmt.Errorf("registry: agent %q not found", nick)
	}

	if !agent.Revoked {
		lockout, err := generatePassphrase()
		if err != nil {
			return fmt.Errorf("registry: generate lockout passphrase: %w", err)
		}
		if err := r.provisioner.ChangePassword(nick, lockout); err != nil {
			return fmt.Errorf("registry: delete lockout: %w", err)
		}
	}

	delete(r.agents, nick)
	r.deleteOne(nick)
	return nil
}

// UpdateChannels replaces the channel list for an active agent.
// Used by relay brokers to sync runtime /join and /part changes back to the registry.
func (r *Registry) UpdateChannels(nick string, channels []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	agent, err := r.get(nick)
	if err != nil {
		return err
	}
	agent.Channels = append([]string(nil), channels...)
	agent.Config.Channels = append([]string(nil), channels...)
	r.saveOne(agent)
	return nil
}

// Get returns the agent with the given nick.
func (r *Registry) Get(nick string) (*Agent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.get(nick)
}

// Touch updates the last-seen timestamp for an agent. Persists to disk
// at most once per minute to avoid thrashing on frequent heartbeats.
func (r *Registry) Touch(nick string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[nick]
	if !ok || a.Revoked {
		return
	}
	now := time.Now()
	shouldPersist := a.LastSeen == nil || now.Sub(*a.LastSeen) >= time.Minute
	a.LastSeen = &now
	if shouldPersist {
		r.saveOne(a)
	}
}

const defaultOnlineTimeout = 2 * time.Minute

// SetOnlineTimeout configures how long since last_seen before an agent
// is considered offline. Pass 0 to reset to the default (2 minutes).
func (r *Registry) SetOnlineTimeout(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onlineTimeout = d
}

func (r *Registry) getOnlineTimeout() time.Duration {
	if r.onlineTimeout > 0 {
		return r.onlineTimeout
	}
	return defaultOnlineTimeout
}

// Reap removes agents that haven't been seen in maxAge. Revoked agents
// are always reaped if older than maxAge. Returns the number of agents removed.
func (r *Registry) Reap(maxAge time.Duration) int {
	if maxAge <= 0 {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	var reaped int
	for nick, a := range r.agents {
		if a.Online {
			continue
		}
		// Use last_seen if available, otherwise fall back to created_at.
		ref := a.CreatedAt
		if a.LastSeen != nil {
			ref = *a.LastSeen
		}
		if ref.Before(cutoff) {
			delete(r.agents, nick)
			if r.db != nil {
				_ = r.db.AgentDelete(nick)
			}
			reaped++
		}
	}
	if reaped > 0 && r.db == nil {
		r.save()
	}
	return reaped
}

// List returns all registered agents with computed online status.
func (r *Registry) List() []*Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	threshold := r.getOnlineTimeout()
	now := time.Now()
	var out []*Agent
	for _, a := range r.agents {
		a.Online = a.LastSeen != nil && now.Sub(*a.LastSeen) < threshold
		out = append(out, a)
	}
	return out
}

func (r *Registry) get(nick string) (*Agent, error) {
	agent, ok := r.agents[nick]
	if !ok {
		return nil, fmt.Errorf("registry: agent %q not found", nick)
	}
	if agent.Revoked {
		return nil, fmt.Errorf("registry: agent %q is revoked", nick)
	}
	return agent, nil
}

func (r *Registry) signPayload(agent *Agent) (*SignedPayload, error) {
	payload := EngagementPayload{
		V:        1,
		Nick:     agent.Nick,
		Type:     agent.Type,
		Config:   agent.Config,
		IssuedAt: time.Now(),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	mac := hmac.New(sha256.New, r.signingKey)
	mac.Write(data)
	sig := hex.EncodeToString(mac.Sum(nil))

	return &SignedPayload{Payload: payload, Signature: sig}, nil
}

// VerifyPayload verifies the HMAC signature on a SignedPayload.
func VerifyPayload(sp *SignedPayload, signingKey []byte) error {
	data, err := json.Marshal(sp.Payload)
	if err != nil {
		return err
	}

	mac := hmac.New(sha256.New, signingKey)
	mac.Write(data)
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sp.Signature), []byte(expected)) {
		return fmt.Errorf("registry: invalid payload signature")
	}
	return nil
}

func generatePassphrase() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
