package systembot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// MemoryStore is an in-memory Store implementation for testing.
type MemoryStore struct {
	mu      sync.Mutex
	entries []Entry
}

func (s *MemoryStore) Append(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
	return nil
}

// All returns a snapshot of all entries.
func (s *MemoryStore) All() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

// FileStore appends entries as JSONL records to a single file in dir. Used in
// production so IRC-system events (joins, parts, quits, mode changes) survive
// systembot restarts. See #167.
type FileStore struct {
	mu  sync.Mutex
	dir string
}

// NewFileStore returns a FileStore writing JSONL to <dir>/systembot.jsonl.
// The directory is created on first append.
func NewFileStore(dir string) *FileStore {
	return &FileStore{dir: dir}
}

// Append writes one Entry as a JSON line to systembot.jsonl. Errors creating
// the directory or writing the line are returned for the caller to log; the
// in-memory state is not affected so a transient disk hiccup doesn't lose
// the bot's view of recent events.
func (s *FileStore) Append(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("systembot: filestore mkdir: %w", err)
	}
	path := filepath.Join(s.dir, "systembot.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("systembot: filestore open: %w", err)
	}
	defer f.Close()
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("systembot: filestore marshal: %w", err)
	}
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("systembot: filestore write: %w", err)
	}
	return nil
}
