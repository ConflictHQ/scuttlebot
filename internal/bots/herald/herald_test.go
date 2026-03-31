package herald_test

import (
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/internal/bots/herald"
)

func newBot(routes herald.RouteConfig) *herald.Bot {
	return herald.New("localhost:6667", "pass", routes, 100, 100, nil)
}

func TestBotName(t *testing.T) {
	b := newBot(herald.RouteConfig{})
	if b.Name() != "herald" {
		t.Errorf("Name(): got %q", b.Name())
	}
}

func TestEmitNonBlocking(t *testing.T) {
	b := newBot(herald.RouteConfig{DefaultChannel: "#fleet"})
	// Fill queue past capacity — should not block.
	for i := 0; i < 300; i++ {
		b.Emit(herald.Event{Type: "ci.build", Message: "build done"})
	}
}

func TestRateLimiterAllows(t *testing.T) {
	// High rate + high burst: all should be allowed immediately.
	b := newBot(herald.RouteConfig{DefaultChannel: "#fleet"})
	// Emit() just queues; actual rate limiting happens in deliver().
	// We test that Emit is non-blocking and the bot is constructible.
	b.Emit(herald.Event{Type: "ci.build", Message: "ok"})
}

func TestRouteConfig(t *testing.T) {
	// Verify routing logic by checking bot construction accepts route maps.
	routes := herald.RouteConfig{
		Routes: map[string]string{
			"ci.":       "#builds",
			"ci.build.": "#builds",
			"deploy.":   "#deploys",
		},
		DefaultChannel: "#alerts",
	}
	b := herald.New("localhost:6667", "pass", routes, 5, 20, nil)
	if b == nil {
		t.Fatal("expected non-nil bot")
	}
}

func TestEmitDropsWhenQueueFull(t *testing.T) {
	b := newBot(herald.RouteConfig{})
	// Emit 1000 events — excess should be dropped without panic or block.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			b.Emit(herald.Event{Type: "x", Message: "y"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Emit blocked or deadlocked")
	}
}
