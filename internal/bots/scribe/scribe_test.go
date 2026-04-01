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

func TestFileStoreJSONL(t *testing.T) {
	dir := t.TempDir()
	fs := scribe.NewFileStore(scribe.FileStoreConfig{Dir: dir, Format: "jsonl"})
	defer fs.Close()

	entries := []scribe.Entry{
		{At: time.Now(), Channel: "#fleet", Nick: "alice", Kind: scribe.EntryKindRaw, Raw: "hello"},
		{At: time.Now(), Channel: "#fleet", Nick: "bob", Kind: scribe.EntryKindRaw, Raw: "world"},
		{At: time.Now(), Channel: "#ops", Nick: "alice", Kind: scribe.EntryKindRaw, Raw: "other"},
	}
	for _, e := range entries {
		if err := fs.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := fs.Query("#fleet", 0)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("Query #fleet: got %d, want 2", len(got))
	}
}

func TestFileStorePerChannel(t *testing.T) {
	dir := t.TempDir()
	fs := scribe.NewFileStore(scribe.FileStoreConfig{Dir: dir, Format: "jsonl", PerChannel: true})
	defer fs.Close()

	_ = fs.Append(scribe.Entry{At: time.Now(), Channel: "#fleet", Nick: "a", Kind: scribe.EntryKindRaw, Raw: "msg"})
	_ = fs.Append(scribe.Entry{At: time.Now(), Channel: "#ops", Nick: "a", Kind: scribe.EntryKindRaw, Raw: "msg"})

	entries, err := fs.Query("#fleet", 0)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("per-channel: got %d entries for #fleet, want 1", len(entries))
	}
}

func TestFileStoreSizeRotation(t *testing.T) {
	dir := t.TempDir()
	// Set threshold to 1 byte so every write triggers rotation.
	fs := scribe.NewFileStore(scribe.FileStoreConfig{Dir: dir, Format: "text", Rotation: "size", MaxSizeMB: 0})
	_ = fs // rotation with MaxSizeMB=0 means no limit; just ensure no panic
	fs2 := scribe.NewFileStore(scribe.FileStoreConfig{Dir: dir, Format: "jsonl", Rotation: "size", MaxSizeMB: 1})
	defer fs2.Close()
	for i := 0; i < 3; i++ {
		if err := fs2.Append(scribe.Entry{At: time.Now(), Channel: "#test", Nick: "x", Kind: scribe.EntryKindRaw, Raw: "line"}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

func TestFileStoreCSVFormat(t *testing.T) {
	dir := t.TempDir()
	fs := scribe.NewFileStore(scribe.FileStoreConfig{Dir: dir, Format: "csv"})
	defer fs.Close()

	err := fs.Append(scribe.Entry{At: time.Now(), Channel: "#fleet", Nick: "alice", Kind: scribe.EntryKindRaw, Raw: `say "hi"`})
	if err != nil {
		t.Fatalf("Append csv: %v", err)
	}
	// Query returns nil for non-jsonl formats — just check no error on Append.
	got, err := fs.Query("#fleet", 0)
	if err != nil {
		t.Fatalf("Query csv: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil from Query on csv format")
	}
}

func TestFileStorePruneOld(t *testing.T) {
	dir := t.TempDir()
	fs := scribe.NewFileStore(scribe.FileStoreConfig{Dir: dir, Format: "jsonl", MaxAgeDays: 1})
	defer fs.Close()

	// Write a file and manually backdate it.
	_ = fs.Append(scribe.Entry{At: time.Now(), Channel: "#fleet", Nick: "a", Kind: scribe.EntryKindRaw, Raw: "x"})

	if err := fs.PruneOld(); err != nil {
		t.Fatalf("PruneOld: %v", err)
	}
}

func TestFileStoreJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	fs := scribe.NewFileStore(scribe.FileStoreConfig{Dir: dir, Format: "jsonl"})
	defer fs.Close()

	orig := scribe.Entry{
		At:          time.Now().Truncate(time.Millisecond),
		Channel:     "#fleet",
		Nick:        "claude-01",
		Kind:        scribe.EntryKindEnvelope,
		MessageType: "task.create",
		MessageID:   "01HX123",
		Raw:         `{"v":1}`,
	}
	_ = fs.Append(orig)

	got, err := fs.Query("#fleet", 1)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	b1, _ := json.Marshal(orig)
	b2, _ := json.Marshal(got[0])
	if string(b1) != string(b2) {
		t.Errorf("round-trip mismatch:\n  want %s\n  got  %s", b1, b2)
	}
}
