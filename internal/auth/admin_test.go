package auth_test

import (
	"path/filepath"
	"testing"

	"github.com/conflicthq/scuttlebot/internal/auth"
)

func newStore(t *testing.T) *auth.AdminStore {
	t.Helper()
	s, err := auth.NewAdminStore(filepath.Join(t.TempDir(), "admins.json"))
	if err != nil {
		t.Fatalf("NewAdminStore: %v", err)
	}
	return s
}

func TestIsEmptyInitially(t *testing.T) {
	s := newStore(t)
	if !s.IsEmpty() {
		t.Error("expected empty store")
	}
}

func TestAddAndAuthenticate(t *testing.T) {
	s := newStore(t)
	if err := s.Add("alice", "s3cr3t"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !s.Authenticate("alice", "s3cr3t") {
		t.Error("expected Authenticate to return true for correct credentials")
	}
	if s.Authenticate("alice", "wrong") {
		t.Error("expected Authenticate to return false for wrong password")
	}
	if s.Authenticate("nobody", "s3cr3t") {
		t.Error("expected Authenticate to return false for unknown user")
	}
}

func TestAddDuplicateReturnsError(t *testing.T) {
	s := newStore(t)
	if err := s.Add("alice", "pass1"); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if err := s.Add("alice", "pass2"); err == nil {
		t.Error("expected error on duplicate Add")
	}
}

func TestIsEmptyAfterAdd(t *testing.T) {
	s := newStore(t)
	_ = s.Add("admin", "pw")
	if s.IsEmpty() {
		t.Error("expected non-empty store after Add")
	}
}

func TestList(t *testing.T) {
	s := newStore(t)
	_ = s.Add("alice", "pw")
	_ = s.Add("bob", "pw")

	list := s.List()
	if len(list) != 2 {
		t.Fatalf("List: got %d, want 2", len(list))
	}
	names := map[string]bool{list[0].Username: true, list[1].Username: true}
	if !names["alice"] || !names["bob"] {
		t.Errorf("List: unexpected names %v", names)
	}
}

func TestSetPassword(t *testing.T) {
	s := newStore(t)
	_ = s.Add("alice", "old")

	if err := s.SetPassword("alice", "new"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if s.Authenticate("alice", "old") {
		t.Error("old password should no longer work")
	}
	if !s.Authenticate("alice", "new") {
		t.Error("new password should work")
	}
}

func TestSetPasswordUnknownUser(t *testing.T) {
	s := newStore(t)
	if err := s.SetPassword("nobody", "pw"); err == nil {
		t.Error("expected error setting password for unknown user")
	}
}

func TestRemove(t *testing.T) {
	s := newStore(t)
	_ = s.Add("alice", "pw")
	_ = s.Add("bob", "pw")

	if err := s.Remove("alice"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(s.List()) != 1 {
		t.Errorf("List after Remove: got %d, want 1", len(s.List()))
	}
	if s.List()[0].Username != "bob" {
		t.Errorf("expected bob to remain, got %q", s.List()[0].Username)
	}
}

func TestRemoveUnknown(t *testing.T) {
	s := newStore(t)
	if err := s.Remove("nobody"); err == nil {
		t.Error("expected error removing unknown user")
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "admins.json")

	s1, err := auth.NewAdminStore(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = s1.Add("alice", "s3cr3t")

	// Load a new store from the same file.
	s2, err := auth.NewAdminStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if s2.IsEmpty() {
		t.Error("reloaded store should not be empty")
	}
	if !s2.Authenticate("alice", "s3cr3t") {
		t.Error("reloaded store should authenticate alice")
	}
}
