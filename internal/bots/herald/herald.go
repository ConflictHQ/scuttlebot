// Package herald implements the herald bot — alert and notification delivery.
//
// External systems push events to herald via Emit(); herald routes them to
// IRC channels based on event type. Supports agent mentions/highlights and
// rate limiting (burst allowed, sustained flood protection).
//
// Event routing is configured per-type in RouteConfig. Unrouted events are
// dropped with a warning log.
package herald

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lrstanley/girc"
)

const botNick = "herald"

// Event is a notification pushed to herald for delivery.
type Event struct {
	// Type identifies the event (e.g. "ci.build.failed", "deploy.complete").
	Type string

	// Channel overrides the default route for this event type.
	// If empty, the RouteConfig default is used.
	Channel string

	// Message is the human-readable notification text.
	Message string

	// MentionNicks are agent nicks to highlight in the message.
	MentionNicks []string
}

// RouteConfig maps event types to IRC channels.
type RouteConfig struct {
	// Routes maps event type prefixes to channels.
	// Key can be an exact type ("ci.build.failed") or a prefix ("ci.").
	// Longest match wins.
	Routes map[string]string

	// DefaultChannel is used when no route matches.
	// If empty, unrouted events are dropped.
	DefaultChannel string
}

// RateLimiter is a simple token-bucket rate limiter.
type RateLimiter struct {
	mu       sync.Mutex
	tokens   float64
	maxBurst float64
	rate     float64 // tokens per second
	last     time.Time
}

func newRateLimiter(ratePerSec float64, burst int) *RateLimiter {
	return &RateLimiter{
		tokens:   float64(burst),
		maxBurst: float64(burst),
		rate:     ratePerSec,
		last:     time.Now(),
	}
}

// Allow returns true if a token is available.
func (r *RateLimiter) Allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(r.last).Seconds()
	r.last = now
	r.tokens = min(r.maxBurst, r.tokens+elapsed*r.rate)
	if r.tokens >= 1 {
		r.tokens--
		return true
	}
	return false
}

// Bot is the herald bot.
type Bot struct {
	ircAddr  string
	password string
	routes   RouteConfig
	limiter  *RateLimiter
	queue    chan Event
	log      *slog.Logger
	client   *girc.Client
}

const defaultQueueSize = 256

// New creates a herald bot. ratePerSec and burst configure the token-bucket
// rate limiter (e.g. 5 messages/sec with burst of 20).
func New(ircAddr, password string, routes RouteConfig, ratePerSec float64, burst int, log *slog.Logger) *Bot {
	if ratePerSec <= 0 {
		ratePerSec = 5
	}
	if burst <= 0 {
		burst = 20
	}
	return &Bot{
		ircAddr:  ircAddr,
		password: password,
		routes:   routes,
		limiter:  newRateLimiter(ratePerSec, burst),
		queue:    make(chan Event, defaultQueueSize),
		log:      log,
	}
}

// Name returns the bot's IRC nick.
func (b *Bot) Name() string { return botNick }

// Emit queues an event for delivery. Non-blocking: drops the event if the
// queue is full and logs a warning.
func (b *Bot) Emit(e Event) {
	select {
	case b.queue <- e:
	default:
		if b.log != nil {
			b.log.Warn("herald: queue full, dropping event", "type", e.Type)
		}
	}
}

// Start connects to IRC and begins processing events. Blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) error {
	host, port, err := splitHostPort(b.ircAddr)
	if err != nil {
		return fmt.Errorf("herald: parse irc addr: %w", err)
	}

	c := girc.New(girc.Config{
		Server:      host,
		Port:        port,
		Nick:        botNick,
		User:        botNick,
		Name:        "scuttlebot herald",
		SASL:        &girc.SASLPlain{User: botNick, Pass: b.password},
		PingDelay:   30 * time.Second,
		PingTimeout: 30 * time.Second,
		SSL:         false,
	})

	c.Handlers.AddBg(girc.CONNECTED, func(_ *girc.Client, _ girc.Event) {
		if b.log != nil {
			b.log.Info("herald connected")
		}
	})

	c.Handlers.AddBg(girc.INVITE, func(cl *girc.Client, e girc.Event) {
		if ch := e.Last(); strings.HasPrefix(ch, "#") {
			cl.Cmd.Join(ch)
		}
	})

	b.client = c

	errCh := make(chan error, 1)
	go func() {
		if err := c.Connect(); err != nil && ctx.Err() == nil {
			errCh <- err
		}
	}()

	// Event delivery loop.
	go b.deliverLoop(ctx)

	select {
	case <-ctx.Done():
		c.Close()
		return nil
	case err := <-errCh:
		return fmt.Errorf("herald: irc connection: %w", err)
	}
}

// Stop disconnects the bot.
func (b *Bot) Stop() {
	if b.client != nil {
		b.client.Close()
	}
}

func (b *Bot) deliverLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-b.queue:
			b.deliver(evt)
		}
	}
}

func (b *Bot) deliver(evt Event) {
	channel := evt.Channel
	if channel == "" {
		channel = b.route(evt.Type)
	}
	if channel == "" {
		if b.log != nil {
			b.log.Warn("herald: no route for event, dropping", "type", evt.Type)
		}
		return
	}

	if !b.limiter.Allow() {
		if b.log != nil {
			b.log.Warn("herald: rate limited, dropping event", "type", evt.Type, "channel", channel)
		}
		return
	}

	msg := evt.Message
	if len(evt.MentionNicks) > 0 {
		msg = strings.Join(evt.MentionNicks, " ") + ": " + msg
	}

	irc := b.client
	if irc != nil {
		irc.Cmd.Message(channel, msg)
	}
}

// route finds the best-matching channel for an event type.
// Longest prefix match wins.
func (b *Bot) route(eventType string) string {
	best := ""
	bestLen := -1
	for prefix, ch := range b.routes.Routes {
		if strings.HasPrefix(eventType, prefix) && len(prefix) > bestLen {
			best = ch
			bestLen = len(prefix)
		}
	}
	if best != "" {
		return best
	}
	return b.routes.DefaultChannel
}

func splitHostPort(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port in %q: %w", addr, err)
	}
	return host, port, nil
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
