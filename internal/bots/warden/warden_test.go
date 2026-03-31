package warden_test

import (
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/internal/bots/warden"
)

func newBot() *warden.Bot {
	return warden.New("localhost:6667", "pass",
		map[string]warden.ChannelConfig{
			"#fleet": {MessagesPerSecond: 5, Burst: 10, CoolDown: 60 * time.Second},
		},
		warden.ChannelConfig{MessagesPerSecond: 2, Burst: 5},
		nil,
	)
}

func TestBotName(t *testing.T) {
	b := newBot()
	if b.Name() != "warden" {
		t.Errorf("Name(): got %q", b.Name())
	}
}

func TestBotNew(t *testing.T) {
	b := newBot()
	if b == nil {
		t.Fatal("expected non-nil bot")
	}
}

func TestChannelConfigDefaults(t *testing.T) {
	// Zero-value config should get sane defaults applied.
	b := warden.New("localhost:6667", "pass",
		nil,
		warden.ChannelConfig{}, // zero — should default
		nil,
	)
	if b == nil {
		t.Fatal("expected non-nil bot")
	}
}

func TestRateLimiterTokenBucket(t *testing.T) {
	// We test the rate limiter logic indirectly through the ChannelConfig.
	// The key invariant: high burst allows initial traffic, sustained rate is enforced.
	// Full rate-limit enforcement requires IRC wiring; this tests construction.
	cfg := warden.ChannelConfig{
		MessagesPerSecond: 10,
		Burst:             20,
		CoolDown:          30 * time.Second,
	}
	b := warden.New("localhost:6667", "pass",
		map[string]warden.ChannelConfig{"#fleet": cfg},
		warden.ChannelConfig{},
		nil,
	)
	if b == nil {
		t.Fatal("expected non-nil bot")
	}
}

func TestActionConstants(t *testing.T) {
	// Ensure action constants are stable.
	actions := map[warden.Action]string{
		warden.ActionWarn: "warn",
		warden.ActionMute: "mute",
		warden.ActionKick: "kick",
	}
	for action, want := range actions {
		if string(action) != want {
			t.Errorf("Action constant: got %q, want %q", action, want)
		}
	}
}
