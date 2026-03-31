package ergo

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/conflicthq/scuttlebot/internal/config"
)

const (
	ircdConfigFile  = "ircd.yaml"
	restartBaseWait = 2 * time.Second
	restartMaxWait  = 60 * time.Second
	healthTimeout   = 30 * time.Second
	healthInterval  = 500 * time.Millisecond
)

// Manager manages the Ergo IRC server subprocess.
type Manager struct {
	cfg config.ErgoConfig
	api *APIClient
	log *slog.Logger
}

// NewManager creates a new Manager. Call Start to launch the Ergo process.
func NewManager(cfg config.ErgoConfig, log *slog.Logger) *Manager {
	return &Manager{
		cfg: cfg,
		api: NewAPIClient(cfg.APIAddr, cfg.APIToken),
		log: log,
	}
}

// API returns the Ergo HTTP API client. Available after Start succeeds.
func (m *Manager) API() *APIClient {
	return m.api
}

// Start manages the Ergo IRC server. In managed mode (the default), it writes
// the Ergo config, starts the subprocess, waits for health, then keeps it
// alive with exponential backoff restarts. In external mode
// (cfg.External=true), it skips subprocess management and simply waits for the
// external ergo instance to become healthy, then blocks until ctx is done.
// Either way, Start blocks until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) error {
	if m.cfg.External {
		return m.startExternal(ctx)
	}
	return m.startManaged(ctx)
}

// startExternal waits for a pre-existing ergo to become healthy, then blocks.
func (m *Manager) startExternal(ctx context.Context) error {
	m.log.Info("ergo external mode — waiting for ergo at", "addr", m.cfg.APIAddr)
	if err := m.waitHealthy(ctx); err != nil {
		return fmt.Errorf("ergo: did not become healthy: %w", err)
	}
	m.log.Info("ergo is healthy (external)")
	<-ctx.Done()
	return nil
}

func (m *Manager) startManaged(ctx context.Context) error {
	if err := m.writeConfig(); err != nil {
		return fmt.Errorf("ergo: write config: %w", err)
	}

	wait := restartBaseWait
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		m.log.Info("starting ergo", "binary", m.cfg.BinaryPath)
		cmd := exec.CommandContext(ctx, m.cfg.BinaryPath, "run", "--conf", m.configPath())
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Dir = m.cfg.DataDir

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("ergo: start process: %w", err)
		}

		if err := m.waitHealthy(ctx); err != nil {
			_ = cmd.Process.Kill()
			return fmt.Errorf("ergo: did not become healthy: %w", err)
		}
		m.log.Info("ergo is healthy")
		wait = restartBaseWait // reset backoff on successful start

		// Wait for process exit.
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		select {
		case <-ctx.Done():
			m.log.Info("shutting down ergo")
			_ = cmd.Process.Signal(os.Interrupt)
			<-done
			return nil
		case err := <-done:
			if ctx.Err() != nil {
				return nil
			}
			m.log.Warn("ergo exited unexpectedly, restarting", "err", err, "wait", wait)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(wait):
			}
			wait = min(wait*2, restartMaxWait)
		}
	}
}

// Rehash reloads the Ergo config. Call after writing a new ircd.yaml.
func (m *Manager) Rehash() error {
	if err := m.writeConfig(); err != nil {
		return fmt.Errorf("ergo: write config: %w", err)
	}
	return m.api.Rehash()
}

func (m *Manager) writeConfig() error {
	if err := os.MkdirAll(m.cfg.DataDir, 0o700); err != nil {
		return err
	}
	data, err := GenerateConfig(m.cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(m.configPath(), data, 0o600)
}

func (m *Manager) configPath() string {
	return filepath.Join(m.cfg.DataDir, ircdConfigFile)
}

func (m *Manager) waitHealthy(ctx context.Context) error {
	deadline := time.Now().Add(healthTimeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := m.api.Status(); err == nil {
			return nil
		}
		time.Sleep(healthInterval)
	}
	return fmt.Errorf("timed out after %s", healthTimeout)
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
