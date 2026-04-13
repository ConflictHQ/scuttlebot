package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateWithTeam(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")

	s, err := NewAPIKeyStore(path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	// Create a key with no team.
	tok1, key1, err := s.Create("no-team", []Scope{ScopeAdmin}, time.Time{}, "")
	if err != nil {
		t.Fatalf("create no-team key: %v", err)
	}
	if tok1 == "" {
		t.Fatal("expected non-empty token")
	}
	if key1.Team != "" {
		t.Errorf("expected empty team, got %q", key1.Team)
	}

	// Create a key with a team.
	tok2, key2, err := s.Create("alpha-key", []Scope{ScopeAgents, ScopeChannels}, time.Time{}, "alpha")
	if err != nil {
		t.Fatalf("create team key: %v", err)
	}
	if tok2 == "" {
		t.Fatal("expected non-empty token")
	}
	if key2.Team != "alpha" {
		t.Errorf("expected team %q, got %q", "alpha", key2.Team)
	}

	// Verify lookup returns the team.
	looked := s.Lookup(tok2)
	if looked == nil {
		t.Fatal("lookup returned nil")
	}
	if looked.Team != "alpha" {
		t.Errorf("lookup: expected team %q, got %q", "alpha", looked.Team)
	}

	// Verify List returns both with correct teams.
	all := s.List()
	if len(all) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(all))
	}
	if all[0].Team != "" {
		t.Errorf("list[0]: expected empty team, got %q", all[0].Team)
	}
	if all[1].Team != "alpha" {
		t.Errorf("list[1]: expected team %q, got %q", "alpha", all[1].Team)
	}
}

func TestTeamPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")

	s, err := NewAPIKeyStore(path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	_, _, err = s.Create("persist-key", []Scope{ScopeAgents}, time.Time{}, "bravo")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Reload from disk.
	s2, err := NewAPIKeyStore(path)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}

	keys := s2.List()
	if len(keys) != 1 {
		t.Fatalf("expected 1 key after reload, got %d", len(keys))
	}
	if keys[0].Team != "bravo" {
		t.Errorf("expected team %q after reload, got %q", "bravo", keys[0].Team)
	}
}

func TestInsertHasNoTeam(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")

	s, err := NewAPIKeyStore(path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	key, err := s.Insert("legacy", "plaintext-token", []Scope{ScopeAdmin})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if key.Team != "" {
		t.Errorf("insert: expected empty team, got %q", key.Team)
	}
}

func TestTestStoreWithTeam(t *testing.T) {
	s := TestStoreWithTeam("admin-tok", "team-tok", []Scope{ScopeAgents, ScopeChannels}, "alpha")

	admin := s.Lookup("admin-tok")
	if admin == nil {
		t.Fatal("admin key not found")
	}
	if admin.Team != "" {
		t.Errorf("admin key should have no team, got %q", admin.Team)
	}

	team := s.Lookup("team-tok")
	if team == nil {
		t.Fatal("team key not found")
	}
	if team.Team != "alpha" {
		t.Errorf("team key: expected team %q, got %q", "alpha", team.Team)
	}
	if !team.HasScope(ScopeAgents) {
		t.Error("team key should have agents scope")
	}
	if !team.HasScope(ScopeChannels) {
		t.Error("team key should have channels scope")
	}
}

func TestBackwardsCompatible(t *testing.T) {
	// Existing keys without a team field should deserialize with empty team.
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")

	// Write a JSON file without the team field (simulates existing data).
	data := `[{"id":"old-key","name":"legacy","hash":"abc","scopes":["admin"],"created_at":"2025-01-01T00:00:00Z","active":true}]`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	s, err := NewAPIKeyStore(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	keys := s.List()
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if keys[0].Team != "" {
		t.Errorf("legacy key should have empty team, got %q", keys[0].Team)
	}
}
