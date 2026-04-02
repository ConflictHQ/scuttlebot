package api

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/conflicthq/scuttlebot/internal/config"
)

// ConfigStore holds the running config and knows how to write it back to disk
// with history snapshots. It is the single write path for all config mutations.
type ConfigStore struct {
	mu         sync.RWMutex
	cfg        config.Config
	path       string // absolute path to scuttlebot.yaml
	historyDir string // where snapshots land
	onChange   []func(config.Config)
}

// NewConfigStore creates a ConfigStore for the given config file path.
// The initial config value is copied in.
func NewConfigStore(path string, cfg config.Config) *ConfigStore {
	histDir := cfg.History.Dir
	if histDir == "" {
		histDir = filepath.Join(cfg.Ergo.DataDir, "config-history")
	}
	return &ConfigStore{
		cfg:        cfg,
		path:       path,
		historyDir: histDir,
	}
}

// Get returns a copy of the current config.
func (s *ConfigStore) Get() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// OnChange registers a callback invoked (in a new goroutine) after every
// successful Save. Multiple callbacks are called concurrently.
func (s *ConfigStore) OnChange(fn func(config.Config)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = append(s.onChange, fn)
}

// Save snapshots the current file, writes next to disk, updates the in-memory
// copy, and fires all OnChange callbacks.
func (s *ConfigStore) Save(next config.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	keep := next.History.Keep
	if keep == 0 {
		keep = 20 // safety fallback; keep=0 in config means "use default"
	}

	// Snapshot before overwrite (no-op if file doesn't exist yet).
	if err := config.SnapshotConfig(s.historyDir, s.path); err != nil {
		return fmt.Errorf("config store: snapshot: %w", err)
	}

	// Write the new config to disk.
	if err := next.Save(s.path); err != nil {
		return fmt.Errorf("config store: save: %w", err)
	}

	// Prune history to keep entries.
	base := filepath.Base(s.path)
	if err := config.PruneHistory(s.historyDir, base, keep); err != nil {
		// Non-fatal: log would be nice but we don't have a logger here.
		// Callers can surface this separately.
		_ = err
	}

	s.cfg = next

	// Fire callbacks outside the lock in fresh goroutines.
	cbs := make([]func(config.Config), len(s.onChange))
	copy(cbs, s.onChange)
	for _, fn := range cbs {
		go fn(next)
	}
	return nil
}

// ListHistory returns the snapshots for the managed config file.
func (s *ConfigStore) ListHistory() ([]config.HistoryEntry, error) {
	s.mu.RLock()
	histDir := s.historyDir
	path := s.path
	s.mu.RUnlock()
	base := filepath.Base(path)
	return config.ListHistory(histDir, base)
}

// ReadHistoryFile returns the raw bytes of a snapshot by filename.
func (s *ConfigStore) ReadHistoryFile(filename string) ([]byte, error) {
	s.mu.RLock()
	histDir := s.historyDir
	s.mu.RUnlock()
	// Sanitize: only allow simple filenames (no path separators).
	if filepath.Base(filename) != filename {
		return nil, fmt.Errorf("config store: invalid history filename")
	}
	return os.ReadFile(filepath.Join(histDir, filename))
}
