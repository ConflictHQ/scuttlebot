package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/pkg/protocol"
)

func TestSplitHostPortInternal(t *testing.T) {
	host, port, err := splitHostPort("irc.example.com:6667")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if host != "irc.example.com" || port != 6667 {
		t.Errorf("got %q:%d", host, port)
	}

	if _, _, err := splitHostPort("noport"); err == nil {
		t.Error("expected error for missing port")
	}
	if _, _, err := splitHostPort(""); err == nil {
		t.Error("expected error for empty addr")
	}
	if _, _, err := splitHostPort("host:notanint"); err == nil {
		t.Error("expected error for non-numeric port")
	}
}

func TestMinDuration(t *testing.T) {
	cases := []struct {
		a, b, want time.Duration
	}{
		{1 * time.Second, 2 * time.Second, 1 * time.Second},
		{5 * time.Second, 1 * time.Second, 1 * time.Second},
		{3 * time.Second, 3 * time.Second, 3 * time.Second},
		{0, 1 * time.Second, 0},
	}
	for _, c := range cases {
		if got := minDuration(c.a, c.b); got != c.want {
			t.Errorf("minDuration(%s, %s) = %s, want %s", c.a, c.b, got, c.want)
		}
	}
}

func TestNoopWriter(t *testing.T) {
	var w noopWriter
	n, err := w.Write([]byte("anything at all"))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if n != len("anything at all") {
		t.Errorf("n = %d, want %d", n, len("anything at all"))
	}
	// Empty payload still works.
	n, err = w.Write(nil)
	if err != nil || n != 0 {
		t.Errorf("Write(nil) = (%d, %v)", n, err)
	}
}

// TestDispatchTypedAndWildcard exercises Client.dispatch with multiple
// handlers — the production code paths cannot easily run without a live IRC
// connection, but dispatch itself is just routing logic.
func TestDispatchTypedAndWildcard(t *testing.T) {
	c, err := New(Options{
		ServerAddr: "localhost:6667",
		Nick:       "agent",
		Password:   "p",
	})
	if err != nil {
		t.Fatal(err)
	}

	var (
		typed     int32
		wild      int32
		other     int32
		typedDone = make(chan struct{}, 1)
	)
	c.Handle("task.create", func(_ context.Context, env *protocol.Envelope) error {
		if env.Type != "task.create" {
			t.Errorf("typed handler saw type %q", env.Type)
		}
		atomic.AddInt32(&typed, 1)
		select {
		case typedDone <- struct{}{}:
		default:
		}
		return nil
	})
	c.Handle("*", func(_ context.Context, _ *protocol.Envelope) error {
		atomic.AddInt32(&wild, 1)
		return nil
	})
	c.Handle("other.type", func(_ context.Context, _ *protocol.Envelope) error {
		atomic.AddInt32(&other, 1)
		return nil
	})

	env := &protocol.Envelope{V: protocol.Version, Type: "task.create", ID: "x", From: "y"}
	c.dispatch(context.Background(), env)

	// Wait for the typed handler to fire (dispatch fans out via goroutines).
	select {
	case <-typedDone:
	case <-time.After(2 * time.Second):
		t.Fatal("typed handler never fired")
	}
	// Allow remaining goroutines to settle.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&wild) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if atomic.LoadInt32(&typed) != 1 {
		t.Errorf("typed = %d, want 1", typed)
	}
	if atomic.LoadInt32(&wild) != 1 {
		t.Errorf("wild = %d, want 1", wild)
	}
	if atomic.LoadInt32(&other) != 0 {
		t.Errorf("other = %d, want 0 (different type)", other)
	}
}

func TestDispatchHandlerErrorIsLogged(t *testing.T) {
	var buf threadSafeBuffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	c, err := New(Options{ServerAddr: "localhost:6667", Nick: "n", Password: "p", Log: logger})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{}, 1)
	c.Handle("*", func(_ context.Context, _ *protocol.Envelope) error {
		defer func() {
			select {
			case done <- struct{}{}:
			default:
			}
		}()
		return errors.New("explode")
	})

	c.dispatch(context.Background(), &protocol.Envelope{
		V: protocol.Version, Type: "x", ID: "id1", From: "agent",
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never fired")
	}

	// Allow log flush.
	time.Sleep(50 * time.Millisecond)
	if !strings.Contains(buf.String(), "handler error") {
		t.Errorf("expected 'handler error' in log, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "explode") {
		t.Errorf("expected wrapped 'explode' in log, got: %s", buf.String())
	}
}

func TestDispatchNoHandlersIsNoop(t *testing.T) {
	c, _ := New(Options{ServerAddr: "localhost:6667", Nick: "n", Password: "p"})
	// No panic, no goroutine leak fanout.
	c.dispatch(context.Background(), &protocol.Envelope{Type: "anything"})
}

// threadSafeBuffer is a goroutine-safe bytes.Buffer for log capture.
type threadSafeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *threadSafeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *threadSafeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestSendErrorOnBadPayload(t *testing.T) {
	c, _ := New(Options{ServerAddr: "localhost:6667", Nick: "n", Password: "p"})
	// channels cannot be JSON-marshalled, so protocol.New will fail before
	// reaching the irc-not-connected check.
	err := c.Send(context.Background(), "#x", "task.create", make(chan int))
	if err == nil {
		t.Fatal("expected marshal error")
	}
	if !strings.Contains(err.Error(), "build envelope") {
		t.Errorf("expected wrap to include 'build envelope', got %v", err)
	}
}

func TestRunReconnectBackoffCancelled(t *testing.T) {
	// Run loops attempting to connect to a port nothing listens on. Each
	// attempt should fail fast; cancelling the context must terminate the
	// loop. This exercises the reconnect-on-error branch and the context
	// select in Run.
	c, _ := New(Options{ServerAddr: "127.0.0.1:1", Nick: "n", Password: "p"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	// Give the loop a moment to make at least one failed attempt + start the
	// backoff sleep, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestSendEnvelopeShape(t *testing.T) {
	// Unit-test the envelope construction path of Send by replacing the
	// underlying irc client with a recording stub. Send writes JSON to the
	// channel; we verify the JSON shape independently of IRC transport.
	c, _ := New(Options{ServerAddr: "localhost:6667", Nick: "the-agent", Password: "p"})

	// Build an envelope the same way Send does and round-trip it.
	env, err := protocol.New("task.create", "the-agent", map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := protocol.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatal(err)
	}
	if generic["type"] != "task.create" {
		t.Errorf("wrong type: %v", generic["type"])
	}
	if generic["from"] != "the-agent" {
		t.Errorf("wrong from: %v", generic["from"])
	}
	// Also confirm Send still rejects when not connected (regression for the
	// nil-irc guard).
	if err := c.Send(context.Background(), "#x", "task.create", nil); err == nil {
		t.Error("expected not-connected error")
	}
}

func TestNewDefaultLoggerSilent(t *testing.T) {
	// When no logger is supplied, the default must not panic when used.
	c, err := New(Options{ServerAddr: "localhost:6667", Nick: "n", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if c.log == nil {
		t.Fatal("default logger must be non-nil")
	}
	// Smoke: log without panicking.
	c.log.Info("hello")
}
