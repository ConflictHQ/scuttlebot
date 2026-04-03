package store

import (
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAgentUpsertAndList(t *testing.T) {
	s := openTest(t)

	cfg := []byte(`{"channels":["#general"]}`)
	r := &AgentRow{
		Nick:      "claude-repo-abc1",
		Type:      "worker",
		Config:    cfg,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		Revoked:   false,
	}

	if err := s.AgentUpsert(r); err != nil {
		t.Fatalf("AgentUpsert: %v", err)
	}

	rows, err := s.AgentList()
	if err != nil {
		t.Fatalf("AgentList: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 agent, got %d", len(rows))
	}
	if rows[0].Nick != r.Nick {
		t.Errorf("nick = %q, want %q", rows[0].Nick, r.Nick)
	}
	if string(rows[0].Config) != string(cfg) {
		t.Errorf("config = %q, want %q", rows[0].Config, cfg)
	}

	// Upsert again with Revoked=true.
	r.Revoked = true
	if err := s.AgentUpsert(r); err != nil {
		t.Fatalf("AgentUpsert (revoke): %v", err)
	}
	rows, err = s.AgentList()
	if err != nil {
		t.Fatalf("AgentList: %v", err)
	}
	if !rows[0].Revoked {
		t.Error("expected revoked=true after upsert")
	}
}

func TestAgentDelete(t *testing.T) {
	s := openTest(t)

	r := &AgentRow{Nick: "test-nick", Type: "worker", Config: []byte(`{}`), CreatedAt: time.Now()}
	if err := s.AgentUpsert(r); err != nil {
		t.Fatal(err)
	}
	if err := s.AgentDelete("test-nick"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.AgentList()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 agents after delete, got %d", len(rows))
	}
}

func TestAdminUpsertListDelete(t *testing.T) {
	s := openTest(t)

	r := &AdminRow{
		Username:  "admin",
		Hash:      []byte("$2a$10$fakehashabcdefghijklmnopqrstuvwx"),
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := s.AdminUpsert(r); err != nil {
		t.Fatalf("AdminUpsert: %v", err)
	}

	rows, err := s.AdminList()
	if err != nil {
		t.Fatalf("AdminList: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 admin, got %d", len(rows))
	}
	if rows[0].Username != "admin" {
		t.Errorf("username = %q, want admin", rows[0].Username)
	}
	if string(rows[0].Hash) != string(r.Hash) {
		t.Errorf("hash mismatch after round-trip")
	}

	if err := s.AdminDelete("admin"); err != nil {
		t.Fatal(err)
	}
	rows, err = s.AdminList()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 admins after delete, got %d", len(rows))
	}
}

func TestPolicyGetSet(t *testing.T) {
	s := openTest(t)

	// No policy yet — should return nil.
	data, err := s.PolicyGet()
	if err != nil {
		t.Fatalf("PolicyGet (empty): %v", err)
	}
	if data != nil {
		t.Errorf("expected nil before first set, got %q", data)
	}

	blob := []byte(`{"behaviors":[]}`)
	if err := s.PolicySet(blob); err != nil {
		t.Fatalf("PolicySet: %v", err)
	}

	got, err := s.PolicyGet()
	if err != nil {
		t.Fatalf("PolicyGet: %v", err)
	}
	if string(got) != string(blob) {
		t.Errorf("PolicyGet = %q, want %q", got, blob)
	}

	// Overwrite.
	blob2 := []byte(`{"behaviors":[{"id":"scribe"}]}`)
	if err := s.PolicySet(blob2); err != nil {
		t.Fatalf("PolicySet (overwrite): %v", err)
	}
	got2, _ := s.PolicyGet()
	if string(got2) != string(blob2) {
		t.Errorf("PolicyGet after overwrite = %q, want %q", got2, blob2)
	}
}
