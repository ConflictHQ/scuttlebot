package systembot

import "sync"

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
