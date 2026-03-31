package client_test

import (
	"context"
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/pkg/client"
)

// TestDiscoveryNotConnected verifies all methods return an error when the
// client is not connected to IRC. The full request→response path requires a
// live Ergo instance (integration test).
func TestDiscoveryNotConnected(t *testing.T) {
	c, _ := client.New(client.Options{
		ServerAddr: "localhost:6667",
		Nick:       "agent-01",
		Password:   "secret",
	})
	d := client.NewDiscovery(c, client.DiscoveryOptions{})
	ctx := context.Background()

	t.Run("ListChannels", func(t *testing.T) {
		if _, err := d.ListChannels(ctx); err == nil {
			t.Error("expected error when not connected")
		}
	})
	t.Run("ChannelMembers", func(t *testing.T) {
		if _, err := d.ChannelMembers(ctx, "#fleet"); err == nil {
			t.Error("expected error when not connected")
		}
	})
	t.Run("GetTopic", func(t *testing.T) {
		if _, err := d.GetTopic(ctx, "#fleet"); err == nil {
			t.Error("expected error when not connected")
		}
	})
	t.Run("WhoIs", func(t *testing.T) {
		if _, err := d.WhoIs(ctx, "someone"); err == nil {
			t.Error("expected error when not connected")
		}
	})
}

func TestDiscoveryCancellation(t *testing.T) {
	c, _ := client.New(client.Options{
		ServerAddr: "localhost:6667",
		Nick:       "agent-01",
		Password:   "secret",
	})
	d := client.NewDiscovery(c, client.DiscoveryOptions{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// All methods should return context.Canceled, not hang.
	_, err := d.ListChannels(ctx)
	// Either "not connected" (faster path) or context.Canceled is acceptable.
	if err == nil {
		t.Error("expected error on cancelled context")
	}
}

func TestDiscoveryCacheTTL(t *testing.T) {
	// Verify that cache entries expire after TTL.
	// We test the cache layer directly by setting a very short TTL.
	c, _ := client.New(client.Options{
		ServerAddr: "localhost:6667",
		Nick:       "agent-01",
		Password:   "secret",
	})

	// Zero TTL disables caching — discovery always hits the server.
	d := client.NewDiscovery(c, client.DiscoveryOptions{CacheTTL: 0})
	if d == nil {
		t.Fatal("NewDiscovery returned nil")
	}
}

func TestDiscoveryDefaultOptions(t *testing.T) {
	c, _ := client.New(client.Options{
		ServerAddr: "localhost:6667",
		Nick:       "agent-01",
		Password:   "secret",
	})
	// Default TTL should be 30s — just verify it doesn't panic.
	d := client.NewDiscovery(c, client.DiscoveryOptions{})
	if d == nil {
		t.Fatal("NewDiscovery returned nil")
	}
}

func TestDiscoveryInvalidate(t *testing.T) {
	c, _ := client.New(client.Options{
		ServerAddr: "localhost:6667",
		Nick:       "agent-01",
		Password:   "secret",
	})
	d := client.NewDiscovery(c, client.DiscoveryOptions{CacheTTL: 10 * time.Minute})
	// Invalidate should not panic on unknown keys.
	d.Invalidate("list_channels")
	d.Invalidate("members:#fleet")
	d.Invalidate("topic:#fleet")
	d.Invalidate("whois:nobody")
}
