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
	// 3 registered (a, b, c), b revoked — List returns all including revoked.
	registered := []string{"a", "b", "c"}
	if len(agents) != len(registered) {
		t.Errorf("List: got %d agents, want %d", len(agents), len(registered))
	}
	var revokedCount int
	for _, a := range agents {
		if a.Revoked {
			revokedCount++
		}
	}
	if revokedCount != 1 {
		t.Errorf("List: got %d revoked, want 1", revokedCount)
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

func TestAdopt(t *testing.T) {
	p := newMockProvisioner()
	r := registry.New(p, testKey)

	payload, err := r.Adopt("preexisting-bot", registry.AgentTypeWorker,
		cfg([]string{"#fleet"}, []string{"read"}))
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if payload.Payload.Nick != "preexisting-bot" {
		t.Errorf("payload Nick = %q, want preexisting-bot", payload.Payload.Nick)
	}
	// Adopt must NOT create a NickServ account (password should be empty in mock).
	if p.passphrase("preexisting-bot") != "" {
		t.Error("Adopt should not create a NickServ account")
	}
	// Agent should be visible in the registry.
	agent, err := r.Get("preexisting-bot")
	if err != nil {
		t.Fatalf("Get after Adopt: %v", err)
	}
	if agent.Nick != "preexisting-bot" {
		t.Errorf("Get Nick = %q", agent.Nick)
	}
}

func TestAdoptDuplicate(t *testing.T) {
	p := newMockProvisioner()
	r := registry.New(p, testKey)

	if _, err := r.Adopt("bot-dup", registry.AgentTypeWorker, registry.EngagementConfig{}); err != nil {
		t.Fatalf("first Adopt: %v", err)
	}
	if _, err := r.Adopt("bot-dup", registry.AgentTypeWorker, registry.EngagementConfig{}); err == nil {
		t.Error("expected error on duplicate Adopt, got nil")
	}
}

func TestDelete(t *testing.T) {
	p := newMockProvisioner()
	r := registry.New(p, testKey)

	if _, _, err := r.Register("del-agent", registry.AgentTypeWorker, registry.EngagementConfig{}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := r.Delete("del-agent"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Agent must no longer appear in List.
	for _, a := range r.List() {
		if a.Nick == "del-agent" {
			t.Error("deleted agent should not appear in List()")
		}
	}

	// Get must fail.
	if _, err := r.Get("del-agent"); err == nil {
		t.Error("Get should fail for deleted agent")
	}
}

func TestDeleteRevoked(t *testing.T) {
	// Deleting a revoked agent should succeed (lockout step skipped).
	p := newMockProvisioner()
	r := registry.New(p, testKey)

	if _, _, err := r.Register("rev-del", registry.AgentTypeWorker, registry.EngagementConfig{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Revoke("rev-del"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := r.Delete("rev-del"); err != nil {
		t.Fatalf("Delete of revoked agent: %v", err)
	}
}

func TestDeleteNotFound(t *testing.T) {
	p := newMockProvisioner()
	r := registry.New(p, testKey)
	if err := r.Delete("nobody"); err == nil {
		t.Error("expected error deleting non-existent agent, got nil")
	}
}

func TestUpdateChannels(t *testing.T) {
	p := newMockProvisioner()
	r := registry.New(p, testKey)

	if _, _, err := r.Register("chan-agent", registry.AgentTypeWorker,
		cfg([]string{"#fleet"}, nil)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	newChans := []string{"#fleet", "#project.foo"}
	if err := r.UpdateChannels("chan-agent", newChans); err != nil {
		t.Fatalf("UpdateChannels: %v", err)
	}

	agent, err := r.Get("chan-agent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(agent.Channels) != 2 {
		t.Errorf("Channels len = %d, want 2", len(agent.Channels))
	}
	if agent.Channels[1] != "#project.foo" {
		t.Errorf("Channels[1] = %q, want #project.foo", agent.Channels[1])
	}
}

func TestUpdateChannelsNotFound(t *testing.T) {
	p := newMockProvisioner()
	r := registry.New(p, testKey)
	if err := r.UpdateChannels("ghost", []string{"#fleet"}); err == nil {
		t.Error("expected error for unknown agent, got nil")
	}
}

func TestSetDataPathPersistence(t *testing.T) {
	dataPath := t.TempDir() + "/agents.json"
	p := newMockProvisioner()
	r := registry.New(p, testKey)

	if err := r.SetDataPath(dataPath); err != nil {
		t.Fatalf("SetDataPath: %v", err)
	}

	if _, _, err := r.Register("persist-me", registry.AgentTypeWorker,
		cfg([]string{"#fleet"}, nil)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// New registry loaded from the same path — must contain the persisted agent.
	r2 := registry.New(newMockProvisioner(), testKey)
	if err := r2.SetDataPath(dataPath); err != nil {
		t.Fatalf("SetDataPath (r2): %v", err)
	}

	agent, err := r2.Get("persist-me")
	if err != nil {
		t.Fatalf("Get after reload: %v", err)
	}
	if agent.Nick != "persist-me" {
		t.Errorf("reloaded Nick = %q, want persist-me", agent.Nick)
	}
}

func TestSetDataPathMissingFileOK(t *testing.T) {
	r := registry.New(newMockProvisioner(), testKey)
	// Path doesn't exist yet — should not error.
	if err := r.SetDataPath(t.TempDir() + "/agents.json"); err != nil {
		t.Errorf("SetDataPath on missing file: %v", err)
	}
}
