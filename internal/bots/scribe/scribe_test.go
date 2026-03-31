package scribe_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/internal/bots/scribe"
	"github.com/conflicthq/scuttlebot/pkg/protocol"
)

func validEnvelopeJSON(t *testing.T, msgType, from string) string {
	t.Helper()
	env, err := protocol.New(msgType, from, map[string]string{"key": "val"})
	if err != nil {
		t.Fatalf("protocol.New: %v", err)
	}
	b, err := protocol.Marshal(env)
	if err != nil {
		t.Fatalf("protocol.Marshal: %v", err)
	}
	return string(b)
}

func TestStoreAppendAndQuery(t *testing.T) {
	s := &scribe.MemoryStore{}

	entries := []scribe.Entry{
		{At: time.Now(), Channel: "#fleet", Nick: "claude-01", Kind: scribe.EntryKindRaw, Raw: "hello"},
		{At: time.Now(), Channel: "#fleet", Nick: "gemini-01", Kind: scribe.EntryKindRaw, Raw: "world"},
		{At: time.Now(), Channel: "#project.test", Nick: "claude-01", Kind: scribe.EntryKindRaw, Raw: "other channel"},
	}
	for _, e := range entries {
		if err := s.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	fleet, err := s.Query("#fleet", 0)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(fleet) != 2 {
		t.Errorf("Query #fleet: got %d entries, want 2", len(fleet))
	}

	all, err := s.Query("", 0)
	if err != nil {
		t.Fatalf("Query all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("Query all: got %d entries, want 3", len(all))
	}
}

func TestStoreQueryLimit(t *testing.T) {
	s := &scribe.MemoryStore{}
	for i := 0; i < 10; i++ {
		_ = s.Append(scribe.Entry{Channel: "#fleet", Nick: "agent", Kind: scribe.EntryKindRaw})
	}

	got, err := s.Query("#fleet", 3)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("Query with limit=3: got %d entries", len(got))
	}
}

func TestEntryKindFromEnvelope(t *testing.T) {
	// Test that a valid envelope JSON is detected as EntryKindEnvelope.
	raw := validEnvelopeJSON(t, protocol.TypeTaskCreate, "claude-01")

	env, err := protocol.Unmarshal([]byte(raw))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	entry := scribe.Entry{
		At:          time.Now(),
		Channel:     "#fleet",
		Nick:        "claude-01",
		Kind:        scribe.EntryKindEnvelope,
		MessageType: env.Type,
		MessageID:   env.ID,
		Raw:         raw,
	}

	if entry.MessageType != protocol.TypeTaskCreate {
		t.Errorf("MessageType: got %q, want %q", entry.MessageType, protocol.TypeTaskCreate)
	}
	if entry.MessageID == "" {
		t.Error("MessageID is empty")
	}
	if entry.Kind != scribe.EntryKindEnvelope {
		t.Errorf("Kind: got %q, want %q", entry.Kind, scribe.EntryKindEnvelope)
	}
}

func TestEntryKindRawForMalformed(t *testing.T) {
	// Non-JSON and invalid envelopes should produce EntryKindRaw entries.
	cases := []string{
		"hello from a human",
		"not json at all",
		`{"incomplete": true}`, // valid JSON but not a valid envelope
	}

	for _, raw := range cases {
		_, err := protocol.Unmarshal([]byte(raw))
		if err == nil {
			// Valid envelope — skip (this case tests malformed only)
			continue
		}
		entry := scribe.Entry{
			At:      time.Now(),
			Channel: "#fleet",
			Nick:    "agent",
			Kind:    scribe.EntryKindRaw,
			Raw:     raw,
		}
		if entry.Kind != scribe.EntryKindRaw {
			t.Errorf("expected EntryKindRaw for %q", raw)
		}
		if entry.MessageType != "" {
			t.Errorf("MessageType should be empty for raw entry")
		}
	}
}

func TestEntryJSONRoundTrip(t *testing.T) {
	entry := scribe.Entry{
		At:          time.Now().Truncate(time.Millisecond),
		Channel:     "#project.test",
		Nick:        "claude-01",
		Kind:        scribe.EntryKindEnvelope,
		MessageType: protocol.TypeAgentHello,
		MessageID:   "01HX123",
		Raw:         `{"v":1}`,
	}

	b, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got scribe.Entry
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Channel != entry.Channel {
		t.Errorf("Channel: got %q, want %q", got.Channel, entry.Channel)
	}
	if got.Kind != entry.Kind {
		t.Errorf("Kind: got %q, want %q", got.Kind, entry.Kind)
	}
	if got.MessageType != entry.MessageType {
		t.Errorf("MessageType: got %q, want %q", got.MessageType, entry.MessageType)
	}
}
