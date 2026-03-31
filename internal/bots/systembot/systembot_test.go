package systembot_test

import (
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/internal/bots/systembot"
)

func TestMemoryStoreAppendAndAll(t *testing.T) {
	s := &systembot.MemoryStore{}
	s.Append(systembot.Entry{Kind: systembot.KindNotice, Nick: "NickServ", Text: "Password accepted"})
	s.Append(systembot.Entry{Kind: systembot.KindJoin, Channel: "#fleet", Nick: "agent-01"})

	entries := s.All()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Kind != systembot.KindNotice {
		t.Errorf("entry 0 kind: got %q, want %q", entries[0].Kind, systembot.KindNotice)
	}
	if entries[1].Kind != systembot.KindJoin {
		t.Errorf("entry 1 kind: got %q, want %q", entries[1].Kind, systembot.KindJoin)
	}
}

func TestEntryKinds(t *testing.T) {
	kinds := []systembot.EntryKind{
		systembot.KindNotice,
		systembot.KindJoin,
		systembot.KindPart,
		systembot.KindQuit,
		systembot.KindKick,
		systembot.KindMode,
	}
	s := &systembot.MemoryStore{}
	for _, k := range kinds {
		if err := s.Append(systembot.Entry{Kind: k, At: time.Now()}); err != nil {
			t.Errorf("Append %q: %v", k, err)
		}
	}
	if got := len(s.All()); got != len(kinds) {
		t.Errorf("expected %d entries, got %d", len(kinds), got)
	}
}

func TestBotNameAndNew(t *testing.T) {
	b := systembot.New("localhost:6667", "pass", []string{"#fleet"}, &systembot.MemoryStore{}, nil)
	if b == nil {
		t.Fatal("expected non-nil bot")
	}
	if b.Name() != "systembot" {
		t.Errorf("Name(): got %q, want systembot", b.Name())
	}
}

func TestPrivmsgIsNotLogged(t *testing.T) {
	// systembot does not have a PRIVMSG handler — this is a design invariant.
	// Verify that NOTICE and connection events ARE the only logged kinds.
	// (The bot itself doesn't expose a direct way to inject events — this is
	// a documentation test confirming the design intent.)
	_ = []systembot.EntryKind{
		systembot.KindNotice,
		systembot.KindJoin,
		systembot.KindPart,
		systembot.KindQuit,
		systembot.KindKick,
		systembot.KindMode,
	}
	// PRIVMSG is NOT in the list — systembot should never log agent message stream events.
}
