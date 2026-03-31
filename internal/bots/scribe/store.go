package scribe

import (
	"sync"
	"time"
)

// EntryKind describes how a log entry was parsed.
type EntryKind string

const (
	EntryKindEnvelope EntryKind = "envelope" // parsed as a valid JSON envelope
	EntryKindRaw      EntryKind = "raw"       // could not be parsed, logged as-is
)

// Entry is a single structured log record written by scribe.
type Entry struct {
	At          time.Time `json:"at"`
	Channel     string    `json:"channel"`
	Nick        string    `json:"nick"`
	Kind        EntryKind `json:"kind"`
	MessageType string    `json:"message_type,omitempty"` // envelope type if Kind == envelope
	MessageID   string    `json:"message_id,omitempty"`   // envelope ID if Kind == envelope
	Raw         string    `json:"raw"`
}

// Store is the storage backend for scribe log entries.
type Store interface {
	Append(entry Entry) error
	Query(channel string, limit int) ([]Entry, error)
}

// MemoryStore is an in-memory Store used for testing.
type MemoryStore struct {
	mu      sync.RWMutex
	entries []Entry
}

func (s *MemoryStore) Append(entry Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
	return nil
}

func (s *MemoryStore) Query(channel string, limit int) ([]Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []Entry
	for _, e := range s.entries {
		if channel == "" || e.Channel == channel {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// All returns all entries (test helper).
func (s *MemoryStore) All() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}
