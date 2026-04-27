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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lrstanley/girc"

	"github.com/conflicthq/scuttlebot/internal/bots/cmdparse"
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
	channels []string
	routes   RouteConfig
	limiter  *RateLimiter
	queue    chan Event
	log      *slog.Logger
	mu       sync.RWMutex // guards client (#168) and counters
	client   *girc.Client

	// Counters for STATUS reporting.
	delivered uint64
	dropped   uint64
}

const defaultQueueSize = 256

// New creates a herald bot. ratePerSec and burst configure the token-bucket
// rate limiter (e.g. 5 messages/sec with burst of 20).
func New(ircAddr, password string, channels []string, routes RouteConfig, ratePerSec float64, burst int, log *slog.Logger) *Bot {
	if ratePerSec <= 0 {
		ratePerSec = 5
	}
	if burst <= 0 {
		burst = 20
	}
	return &Bot{
		ircAddr:  ircAddr,
		password: password,
		channels: channels,
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
		b.mu.Lock()
		b.dropped++
		b.mu.Unlock()
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

	c.Handlers.AddBg(girc.CONNECTED, func(cl *girc.Client, _ girc.Event) {
		cl.Cmd.Mode(cl.GetNick(), "+B")
		for _, ch := range b.channels {
			cl.Cmd.Join(ch)
		}
		if b.log != nil {
			b.log.Info("herald connected", "channels", b.channels)
		}
	})

	c.Handlers.AddBg(girc.INVITE, func(cl *girc.Client, e girc.Event) {
		if ch := e.Last(); strings.HasPrefix(ch, "#") {
			cl.Cmd.Join(ch)
		}
	})

	router := cmdparse.NewRouter(botNick)
	router.SetPurpose("the notification bot — routes external events into IRC channels")
	router.Register(cmdparse.Command{
		Name:        "status",
		Usage:       "STATUS",
		Description: "show route count, queue depth, and delivery counters",
		Handler: func(_ *cmdparse.Context, _ string) string {
			return b.cmdStatus()
		},
	})
	router.Register(cmdparse.Command{
		Name:        "routes",
		Usage:       "ROUTES",
		Description: "list configured event-type routes",
		Handler: func(_ *cmdparse.Context, _ string) string {
			return b.cmdRoutes()
		},
	})
	router.Register(cmdparse.Command{
		Name:        "test",
		Usage:       "TEST [#channel]",
		Description: "send a test notification (defaults to current channel)",
		Handler: func(ctx *cmdparse.Context, args string) string {
			return b.cmdTest(ctx, args)
		},
	})

	c.Handlers.AddBg(girc.PRIVMSG, func(cl *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil {
			return
		}
		// Dispatch commands (DMs and channel messages).
		if reply := router.Dispatch(e.Source.Name, e.Params[0], e.Last()); reply != nil {
			for _, line := range reply.Lines() {
				cl.Cmd.Message(reply.Target, line)
			}
			return
		}
	})

	b.mu.Lock()
	b.client = c
	b.mu.Unlock()

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
		b.mu.Lock()
		b.dropped++
		b.mu.Unlock()
		if b.log != nil {
			b.log.Warn("herald: no route for event, dropping", "type", evt.Type)
		}
		return
	}

	if !b.limiter.Allow() {
		b.mu.Lock()
		b.dropped++
		b.mu.Unlock()
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
		b.mu.Lock()
		b.delivered++
		b.mu.Unlock()
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

func (b *Bot) cmdStatus() string {
	b.mu.RLock()
	delivered, dropped := b.delivered, b.dropped
	b.mu.RUnlock()
	queued := len(b.queue)
	return fmt.Sprintf("herald: %d route(s), default=%s | queue %d/%d | delivered %d, dropped %d",
		len(b.routes.Routes), b.routes.DefaultChannel, queued, cap(b.queue), delivered, dropped)
}

func (b *Bot) cmdRoutes() string {
	if len(b.routes.Routes) == 0 && b.routes.DefaultChannel == "" {
		return "herald: no routes configured"
	}
	prefixes := make([]string, 0, len(b.routes.Routes))
	for p := range b.routes.Routes {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)

	var sb strings.Builder
	sb.WriteString("herald routes (longest prefix wins):\n")
	for _, p := range prefixes {
		sb.WriteString(fmt.Sprintf("  %-30s → %s\n", p, b.routes.Routes[p]))
	}
	if b.routes.DefaultChannel != "" {
		sb.WriteString(fmt.Sprintf("  %-30s → %s", "(default)", b.routes.DefaultChannel))
	} else {
		sb.WriteString("  (no default — unrouted events drop)")
	}
	return sb.String()
}

// cmdTest enqueues a synthetic event so operators can verify herald is wired
// up end-to-end without firing real webhooks. Picks a target channel from the
// argument, the current channel context, or DefaultChannel — in that order.
func (b *Bot) cmdTest(ctx *cmdparse.Context, args string) string {
	channel := strings.TrimSpace(args)
	if channel == "" {
		channel = ctx.Channel
	}
	if channel == "" {
		channel = b.routes.DefaultChannel
	}
	if channel == "" {
		return "herald: no target channel — pass #channel as argument"
	}
	b.Emit(Event{
		Type:    "test.herald",
		Channel: channel,
		Message: fmt.Sprintf("herald TEST event from %s", ctx.Nick),
	})
	return fmt.Sprintf("herald: queued TEST event to %s", channel)
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
