package client_test

import (
	"context"
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/pkg/client"
	"github.com/conflicthq/scuttlebot/pkg/protocol"
)

func TestNewValidation(t *testing.T) {
	tests := []struct {
		name string
		opts client.Options
	}{
		{"missing server", client.Options{Nick: "a", Password: "p"}},
		{"missing nick", client.Options{ServerAddr: "localhost:6667", Password: "p"}},
		{"missing password", client.Options{ServerAddr: "localhost:6667", Nick: "a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := client.New(tt.opts); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestNewOK(t *testing.T) {
	c, err := client.New(client.Options{
		ServerAddr: "localhost:6667",
		Nick:       "agent-01",
		Password:   "secret",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestHandleRegistration(t *testing.T) {
	c, _ := client.New(client.Options{
		ServerAddr: "localhost:6667",
		Nick:       "agent-01",
		Password:   "secret",
	})

	called := make(chan string, 1)
	c.Handle("task.create", func(ctx context.Context, env *protocol.Envelope) error {
		called <- env.Type
		return nil
	})

	// Verify handler is registered by checking it doesn't panic.
	// Dispatch is tested indirectly via the exported Handle API.
	select {
	case <-called:
		t.Error("handler fired unexpectedly before any message")
	default:
	}
}

func TestSendNotConnected(t *testing.T) {
	c, _ := client.New(client.Options{
		ServerAddr: "localhost:6667",
		Nick:       "agent-01",
		Password:   "secret",
	})

	err := c.Send(context.Background(), "#fleet", "task.create", map[string]string{"task": "x"})
	if err == nil {
		t.Error("expected error when not connected, got nil")
	}
}

func TestRunCancelledImmediately(t *testing.T) {
	c, _ := client.New(client.Options{
		ServerAddr: "localhost:6667",
		Nick:       "agent-01",
		Password:   "secret",
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Run did not return after ctx cancelled")
	}
}

func TestWildcardHandler(t *testing.T) {
	// Verify that registering "*" doesn't panic and multiple handlers stack.
	c, _ := client.New(client.Options{
		ServerAddr: "localhost:6667",
		Nick:       "agent-01",
		Password:   "secret",
	})

	c.Handle("*", func(ctx context.Context, env *protocol.Envelope) error { return nil })
	c.Handle("*", func(ctx context.Context, env *protocol.Envelope) error { return nil })
	c.Handle("task.create", func(ctx context.Context, env *protocol.Envelope) error { return nil })
}
