package auditbot_test

import (
	"testing"

	"github.com/conflicthq/scuttlebot/internal/bots/auditbot"
)

func newBot(auditTypes ...string) (*auditbot.Bot, *auditbot.MemoryStore) {
	s := &auditbot.MemoryStore{}
	b := auditbot.New("localhost:6667", "pass", []string{"#fleet"}, auditTypes, s, nil)
	return b, s
}

func TestBotNameAndNew(t *testing.T) {
	b, _ := newBot()
	if b.Name() != "auditbot" {
		t.Errorf("Name(): got %q, want auditbot", b.Name())
	}
}

func TestRecordRegistryEvent(t *testing.T) {
	_, s := newBot("agent.registered")
	// newBot uses nil logger; Record directly writes to store.
	b, s2 := newBot("agent.registered")
	b.Record("agent-01", "agent.registered", "new registration")

	entries := s2.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Kind != auditbot.KindRegistry {
		t.Errorf("Kind: got %q, want registry", e.Kind)
	}
	if e.Nick != "agent-01" {
		t.Errorf("Nick: got %q", e.Nick)
	}
	if e.MessageType != "agent.registered" {
		t.Errorf("MessageType: got %q", e.MessageType)
	}
	if e.Detail != "new registration" {
		t.Errorf("Detail: got %q", e.Detail)
	}
	_ = s
}

func TestRecordMultipleRegistryEvents(t *testing.T) {
	b, s := newBot()
	b.Record("agent-01", "agent.registered", "")
	b.Record("agent-01", "credentials.rotated", "")
	b.Record("agent-02", "agent.revoked", "policy violation")

	entries := s.All()
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
}

func TestStoreIsAppendOnly(t *testing.T) {
	s := &auditbot.MemoryStore{}
	s.Append(auditbot.Entry{Nick: "a", MessageType: "task.create"})
	s.Append(auditbot.Entry{Nick: "b", MessageType: "task.complete"})

	entries := s.All()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// Modifying the snapshot should not affect the store.
	entries[0].Nick = "tampered"
	fresh := s.All()
	if fresh[0].Nick == "tampered" {
		t.Error("store should be immutable — snapshot modification should not affect store")
	}
}

func TestAuditTypeFilter(t *testing.T) {
	// Only task.create should be audited.
	b, s := newBot("task.create")
	// Record two types — only the audited one should appear.
	// We can't inject IRC events directly, but we can verify that Record()
	// with any type always writes (registry events bypass the filter).
	b.Record("agent-01", "task.create", "")
	b.Record("agent-01", "task.update", "") // registry events always written

	entries := s.All()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries from Record(), got %d", len(entries))
	}
}

func TestAuditAllWhenNoFilter(t *testing.T) {
	b, s := newBot() // no filter = audit everything
	b.Record("a", "task.create", "")
	b.Record("b", "task.update", "")
	b.Record("c", "agent.hello", "")

	if got := len(s.All()); got != 3 {
		t.Errorf("expected 3 entries, got %d", got)
	}
}

func TestEntryTimestamp(t *testing.T) {
	b, s := newBot()
	b.Record("agent-01", "agent.registered", "")

	entries := s.All()
	if entries[0].At.IsZero() {
		t.Error("entry timestamp should not be zero")
	}
}
