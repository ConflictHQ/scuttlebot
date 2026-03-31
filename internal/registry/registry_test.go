package registry_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/conflicthq/scuttlebot/internal/registry"
)

// mockProvisioner records calls for test assertions.
type mockProvisioner struct {
	mu       sync.Mutex
	accounts map[string]string // nick → passphrase
}

func newMockProvisioner() *mockProvisioner {
	return &mockProvisioner{accounts: make(map[string]string)}
}

func (m *mockProvisioner) RegisterAccount(name, passphrase string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.accounts[name]; exists {
		return fmt.Errorf("ACCOUNT_EXISTS")
	}
	m.accounts[name] = passphrase
	return nil
}

func (m *mockProvisioner) ChangePassword(name, passphrase string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.accounts[name]; !exists {
		return fmt.Errorf("ACCOUNT_DOES_NOT_EXIST")
	}
	m.accounts[name] = passphrase
	return nil
}

func (m *mockProvisioner) passphrase(nick string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.accounts[nick]
}

var testKey = []byte("test-signing-key-do-not-use-in-production")

func cfg(channels, permissions []string) registry.EngagementConfig {
	return registry.EngagementConfig{Channels: channels, Permissions: permissions}
}

func TestRegister(t *testing.T) {
	p := newMockProvisioner()
	r := registry.New(p, testKey)

	creds, payload, err := r.Register("claude-01", registry.AgentTypeWorker,
		cfg([]string{"#fleet", "#project.test"}, []string{"task.create"}))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if creds.Nick != "claude-01" {
		t.Errorf("Nick: got %q, want %q", creds.Nick, "claude-01")
	}
	if creds.Passphrase == "" {
		t.Error("Passphrase is empty")
	}
	if p.passphrase("claude-01") == "" {
		t.Error("account not created in provisioner")
	}
	if payload.Payload.Nick != "claude-01" {
		t.Errorf("payload Nick: got %q", payload.Payload.Nick)
	}
	if payload.Signature == "" {
		t.Error("payload signature is empty")
	}
	if len(payload.Payload.Config.Channels) != 2 {
		t.Errorf("payload channels: got %d, want 2", len(payload.Payload.Config.Channels))
	}
}

func TestRegisterDuplicate(t *testing.T) {
	p := newMockProvisioner()
	r := registry.New(p, testKey)

	if _, _, err := r.Register("agent-01", registry.AgentTypeWorker, registry.EngagementConfig{}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if _, _, err := r.Register("agent-01", registry.AgentTypeWorker, registry.EngagementConfig{}); err == nil {
		t.Error("expected error on duplicate registration, got nil")
	}
}

func TestRotate(t *testing.T) {
	p := newMockProvisioner()
	r := registry.New(p, testKey)

	creds, _, err := r.Register("agent-02", registry.AgentTypeWorker, registry.EngagementConfig{})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	original := creds.Passphrase

	newCreds, err := r.Rotate("agent-02")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if newCreds.Passphrase == original {
		t.Error("passphrase should change after rotation")
	}
	if p.passphrase("agent-02") != newCreds.Passphrase {
		t.Error("provisioner passphrase should match rotated credentials")
	}
}

func TestRevoke(t *testing.T) {
	p := newMockProvisioner()
	r := registry.New(p, testKey)

	creds, _, err := r.Register("agent-03", registry.AgentTypeWorker, registry.EngagementConfig{})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := r.Revoke("agent-03"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	if p.passphrase("agent-03") == creds.Passphrase {
		t.Error("passphrase should change after revocation")
	}
	if _, err := r.Get("agent-03"); err == nil {
		t.Error("Get should fail for revoked agent")
	}
}

func TestVerifyPayload(t *testing.T) {
	p := newMockProvisioner()
	r := registry.New(p, testKey)

	_, payload, err := r.Register("agent-04", registry.AgentTypeOrchestrator,
		cfg([]string{"#fleet"}, []string{"task.create", "task.assign"}))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := registry.VerifyPayload(payload, testKey); err != nil {
		t.Errorf("VerifyPayload: %v", err)
	}

	// Tamper with the payload.
	payload.Payload.Nick = "evil-agent"
	if err := registry.VerifyPayload(payload, testKey); err == nil {
		t.Error("VerifyPayload should fail after tampering")
	}
}

func TestList(t *testing.T) {
	p := newMockProvisioner()
	r := registry.New(p, testKey)

	for _, nick := range []string{"a", "b", "c"} {
		if _, _, err := r.Register(nick, registry.AgentTypeWorker, registry.EngagementConfig{}); err != nil {
			t.Fatalf("Register %q: %v", nick, err)
		}
	}
	if err := r.Revoke("b"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	agents := r.List()
	if len(agents) != 2 {
		t.Errorf("List: got %d agents, want 2 (revoked should be excluded)", len(agents))
	}
}

func TestEngagementConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     registry.EngagementConfig
		wantErr bool
	}{
		{
			name: "valid full config",
			cfg: registry.EngagementConfig{
				Channels:    []string{"#fleet", "#project.test"},
				OpsChannels: []string{"#fleet"},
				Permissions: []string{"task.create"},
				RateLimit:   registry.RateLimitConfig{MessagesPerSecond: 10, Burst: 20},
				Rules: registry.EngagementRules{
					RespondToTypes: []string{"task.create"},
					IgnoreNicks:    []string{"scribe"},
				},
			},
			wantErr: false,
		},
		{
			name:    "empty config is valid",
			cfg:     registry.EngagementConfig{},
			wantErr: false,
		},
		{
			name:    "channel missing hash",
			cfg:     registry.EngagementConfig{Channels: []string{"fleet"}},
			wantErr: true,
		},
		{
			name:    "channel with space",
			cfg:     registry.EngagementConfig{Channels: []string{"#fleet channel"}},
			wantErr: true,
		},
		{
			name:    "ops_channel not in channels",
			cfg:     registry.EngagementConfig{Channels: []string{"#fleet"}, OpsChannels: []string{"#other"}},
			wantErr: true,
		},
		{
			name:    "negative rate limit",
			cfg:     registry.EngagementConfig{RateLimit: registry.RateLimitConfig{MessagesPerSecond: -1}},
			wantErr: true,
		},
		{
			name:    "negative burst",
			cfg:     registry.EngagementConfig{RateLimit: registry.RateLimitConfig{Burst: -5}},
			wantErr: true,
		},
		{
			name:    "empty respond_to_type",
			cfg:     registry.EngagementConfig{Rules: registry.EngagementRules{RespondToTypes: []string{""}}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRegisterInvalidConfig(t *testing.T) {
	p := newMockProvisioner()
	r := registry.New(p, testKey)

	_, _, err := r.Register("bad-agent", registry.AgentTypeWorker, registry.EngagementConfig{
		Channels: []string{"no-hash-here"},
	})
	if err == nil {
		t.Error("expected error for invalid channel name, got nil")
	}
	// Account should not have been created.
	if p.passphrase("bad-agent") != "" {
		t.Error("account should not be created when config is invalid")
	}
}
