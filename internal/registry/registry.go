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
	"sync"
	"time"
)

// AgentType describes an agent's role and authority level.
type AgentType string

const (
	AgentTypeOrchestrator AgentType = "orchestrator" // +o in channels
	AgentTypeWorker       AgentType = "worker"        // +v in channels
	AgentTypeObserver     AgentType = "observer"      // no special mode
)

// Agent is a registered agent.
type Agent struct {
	Nick        string    `json:"nick"`
	Type        AgentType `json:"type"`
	Channels    []string  `json:"channels"`
	Permissions []string  `json:"permissions"`
	CreatedAt   time.Time `json:"created_at"`
	Revoked     bool      `json:"revoked"`
}

// Credentials are the SASL credentials an agent uses to connect to Ergo.
type Credentials struct {
	Nick       string `json:"nick"`
	Passphrase string `json:"passphrase"`
}

// EngagementPayload is the signed payload delivered to an agent on registration.
// It describes the agent's channel assignments, permissions, and engagement rules.
type EngagementPayload struct {
	V           int       `json:"v"`
	Nick        string    `json:"nick"`
	Type        AgentType `json:"type"`
	Channels    []string  `json:"channels"`
	Permissions []string  `json:"permissions"`
	IssuedAt    time.Time `json:"issued_at"`
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
	mu          sync.RWMutex
	agents      map[string]*Agent // keyed by nick
	provisioner AccountProvisioner
	signingKey  []byte
}

// New creates a new Registry with the given provisioner and HMAC signing key.
func New(provisioner AccountProvisioner, signingKey []byte) *Registry {
	return &Registry{
		agents:      make(map[string]*Agent),
		provisioner: provisioner,
		signingKey:  signingKey,
	}
}

// Register creates a new agent, provisions its Ergo account, and returns
// credentials and a signed rules-of-engagement payload.
func (r *Registry) Register(nick string, agentType AgentType, channels, permissions []string) (*Credentials, *SignedPayload, error) {
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
		return nil, nil, fmt.Errorf("registry: provision account: %w", err)
	}

	agent := &Agent{
		Nick:        nick,
		Type:        agentType,
		Channels:    channels,
		Permissions: permissions,
		CreatedAt:   time.Now(),
	}
	r.agents[nick] = agent

	payload, err := r.signPayload(agent)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: sign payload: %w", err)
	}

	return &Credentials{Nick: nick, Passphrase: passphrase}, payload, nil
}

// Rotate generates a new passphrase for an agent and updates Ergo.
func (r *Registry) Rotate(nick string) (*Credentials, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	agent, err := r.get(nick)
	if err != nil {
		return nil, err
	}

	passphrase, err := generatePassphrase()
	if err != nil {
		return nil, fmt.Errorf("registry: generate passphrase: %w", err)
	}

	if err := r.provisioner.ChangePassword(nick, passphrase); err != nil {
		return nil, fmt.Errorf("registry: rotate credentials: %w", err)
	}

	_ = agent // agent exists, credentials rotated
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
	return nil
}

// Get returns the agent with the given nick.
func (r *Registry) Get(nick string) (*Agent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.get(nick)
}

// List returns all registered, non-revoked agents.
func (r *Registry) List() []*Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Agent
	for _, a := range r.agents {
		if !a.Revoked {
			out = append(out, a)
		}
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
		V:           1,
		Nick:        agent.Nick,
		Type:        agent.Type,
		Channels:    agent.Channels,
		Permissions: agent.Permissions,
		IssuedAt:    time.Now(),
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
