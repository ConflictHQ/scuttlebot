package manager_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/conflicthq/scuttlebot/internal/bots/manager"
	"log/slog"
)

var testLog = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

// stubProvisioner records RegisterAccount/ChangePassword calls.
type stubProvisioner struct {
	accounts map[string]string
	failOn   string // if set, RegisterAccount returns an error for this nick
}

func newStub() *stubProvisioner {
	return &stubProvisioner{accounts: make(map[string]string)}
}

func (p *stubProvisioner) RegisterAccount(name, pass string) error {
	if p.failOn == name {
		return fmt.Errorf("ACCOUNT_EXISTS")
	}
	if _, ok := p.accounts[name]; ok {
		return fmt.Errorf("ACCOUNT_EXISTS")
	}
	p.accounts[name] = pass
	return nil
}

func (p *stubProvisioner) ChangePassword(name, pass string) error {
	if _, ok := p.accounts[name]; !ok {
		return fmt.Errorf("ACCOUNT_DOES_NOT_EXIST")
	}
	p.accounts[name] = pass
	return nil
}

// stubChannels returns a fixed list of channels.
type stubChannels struct {
	channels []string
	err      error
}

func (c *stubChannels) ListChannels() ([]string, error) {
	return c.channels, c.err
}

func newManager(t *testing.T) *manager.Manager {
	t.Helper()
	return manager.New(
		"127.0.0.1:6667",
		t.TempDir(),
		newStub(),
		&stubChannels{channels: []string{"#fleet", "#ops"}},
		testLog,
	)
}

// scribeSpec returns a minimal enabled scribe BotSpec.
func scribeSpec() manager.BotSpec {
	return manager.BotSpec{
		ID:      "scribe",
		Nick:    "scribe",
		Enabled: true,
		Config:  map[string]any{"dir": "/tmp/scribe-test-logs"},
	}
}

func TestSyncStartsEnabledBot(t *testing.T) {
	m := newManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m.Sync(ctx, []manager.BotSpec{scribeSpec()})

	running := m.Running()
	if len(running) != 1 || running[0] != "scribe" {
		t.Errorf("expected [scribe] running, got %v", running)
	}
}

func TestSyncDisabledBotNotStarted(t *testing.T) {
	m := newManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spec := scribeSpec()
	spec.Enabled = false
	m.Sync(ctx, []manager.BotSpec{spec})

	if len(m.Running()) != 0 {
		t.Errorf("expected no bots running, got %v", m.Running())
	}
}

func TestSyncStopsDisabledBot(t *testing.T) {
	m := newManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start it.
	m.Sync(ctx, []manager.BotSpec{scribeSpec()})
	if len(m.Running()) != 1 {
		t.Fatalf("bot should be running before disable")
	}

	// Disable it.
	spec := scribeSpec()
	spec.Enabled = false
	m.Sync(ctx, []manager.BotSpec{spec})

	if len(m.Running()) != 0 {
		t.Errorf("expected bot stopped after disable, got %v", m.Running())
	}
}

func TestSyncIdempotent(t *testing.T) {
	m := newManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spec := scribeSpec()
	m.Sync(ctx, []manager.BotSpec{spec})
	m.Sync(ctx, []manager.BotSpec{spec}) // second call — should not start a second copy

	if len(m.Running()) != 1 {
		t.Errorf("expected exactly 1 running bot, got %v", m.Running())
	}
}

func TestPasswordPersistence(t *testing.T) {
	dir := t.TempDir()
	prov := newStub()
	m1 := manager.New("127.0.0.1:6667", dir, prov, &stubChannels{}, testLog)

	ctx, cancel := context.WithCancel(context.Background())
	m1.Sync(ctx, []manager.BotSpec{scribeSpec()})
	cancel()

	// Passwords file should exist.
	pwPath := filepath.Join(dir, "bot_passwords.json")
	if _, err := os.Stat(pwPath); err != nil {
		t.Fatalf("passwords file not created: %v", err)
	}

	// Load a second manager from the same dir — it should reuse the same password
	// (ensureAccount will call ChangePassword, not RegisterAccount, because the stub
	// already has the account from the first run).
	m2 := manager.New("127.0.0.1:6667", dir, prov, &stubChannels{}, testLog)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	// Should not panic and should be able to start the bot.
	m2.Sync(ctx2, []manager.BotSpec{scribeSpec()})
	if len(m2.Running()) != 1 {
		t.Errorf("second manager: expected 1 running bot, got %v", m2.Running())
	}
}

func TestSyncOracleStarts(t *testing.T) {
	// Oracle now starts with default config (no API key — it won't respond to
	// summaries but the bot itself connects to IRC and runs).
	m := newManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spec := manager.BotSpec{ID: "oracle", Nick: "oracle", Enabled: true}
	m.Sync(ctx, []manager.BotSpec{spec})

	running := m.Running()
	found := false
	for _, nick := range running {
		if nick == "oracle" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected oracle to be in Running, got %v", running)
	}
}

func TestSyncMultipleBots(t *testing.T) {
	m := newManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	specs := []manager.BotSpec{
		scribeSpec(),
		{ID: "snitch", Nick: "snitch", Enabled: true},
	}
	m.Sync(ctx, []manager.BotSpec{specs[0], specs[1]})

	running := m.Running()
	if len(running) != 2 {
		t.Errorf("expected 2 running bots, got %v", running)
	}
}
