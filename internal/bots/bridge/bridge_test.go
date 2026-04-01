package bridge

import (
	"testing"
	"time"
)

func TestUsersFiltersAndPrunesExpiredWebUsers(t *testing.T) {
	b := &Bot{
		nick:       "bridge",
		webUserTTL: 5 * time.Minute,
		webUsers: map[string]map[string]time.Time{
			"#general": {
				"recent-user": time.Now().Add(-2 * time.Minute),
				"stale-user":  time.Now().Add(-10 * time.Minute),
			},
		},
	}

	got := b.Users("#general")
	if len(got) != 1 || got[0] != "recent-user" {
		t.Fatalf("Users() = %v, want [recent-user]", got)
	}

	if _, ok := b.webUsers["#general"]["stale-user"]; ok {
		t.Fatalf("stale-user was not pruned from webUsers")
	}
}

func TestSetWebUserTTLDefaultsNonPositiveValues(t *testing.T) {
	b := &Bot{}

	b.SetWebUserTTL(0)
	if b.webUserTTL != defaultWebUserTTL {
		t.Fatalf("SetWebUserTTL(0) = %v, want %v", b.webUserTTL, defaultWebUserTTL)
	}

	b.SetWebUserTTL(-1 * time.Minute)
	if b.webUserTTL != defaultWebUserTTL {
		t.Fatalf("SetWebUserTTL(-1m) = %v, want %v", b.webUserTTL, defaultWebUserTTL)
	}
}

func TestTouchUserMarksNickActive(t *testing.T) {
	b := &Bot{webUsers: make(map[string]map[string]time.Time)}

	b.TouchUser("#general", "codex-test")

	if b.webUsers["#general"] == nil {
		t.Fatal("TouchUser did not initialize channel map")
	}
	if _, ok := b.webUsers["#general"]["codex-test"]; !ok {
		t.Fatal("TouchUser did not record nick")
	}
}
