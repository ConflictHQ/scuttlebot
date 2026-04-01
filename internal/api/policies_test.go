package api

import (
	"path/filepath"
	"testing"
)

func TestNewPolicyStoreDefaultsBridgeTTL(t *testing.T) {
	ps, err := NewPolicyStore(filepath.Join(t.TempDir(), "policies.json"), 5)
	if err != nil {
		t.Fatalf("NewPolicyStore() error = %v", err)
	}

	got := ps.Get().Bridge.WebUserTTLMinutes
	if got != 5 {
		t.Fatalf("default bridge ttl = %d, want 5", got)
	}
}

func TestPolicyStoreSetNormalizesBridgeTTL(t *testing.T) {
	ps, err := NewPolicyStore(filepath.Join(t.TempDir(), "policies.json"), 5)
	if err != nil {
		t.Fatalf("NewPolicyStore() error = %v", err)
	}

	p := ps.Get()
	p.Bridge.WebUserTTLMinutes = 0
	if err := ps.Set(p); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	got := ps.Get().Bridge.WebUserTTLMinutes
	if got != 5 {
		t.Fatalf("normalized bridge ttl = %d, want 5", got)
	}
}
