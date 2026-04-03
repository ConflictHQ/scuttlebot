// Package warden implements the warden bot — channel moderation and rate limiting.
//
// warden monitors channels for misbehaving agents:
//   - Malformed message envelopes → NOTICE to sender
//   - Excessive message rates → warn (NOTICE), then mute (+q), then kick
//
// Actions escalate: first violation warns, second mutes, third kicks.
// Escalation state resets after a configurable cool-down period.
package warden

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

	"github.com/conflicthq/scuttlebot/pkg/protocol"
)

const botNick = "warden"

// Action is an enforcement action taken against a nick.
type Action string

const (
	ActionWarn Action = "warn"
	ActionMute Action = "mute"
	ActionKick Action = "kick"
)

// ChannelConfig configures warden's limits for a single channel.
type ChannelConfig struct {
	// MessagesPerSecond is the max sustained rate. Default: 5.
	MessagesPerSecond float64

	// Burst is the max burst above the rate. Default: 10.
	Burst int

	// CoolDown is how long before escalation state resets. Default: 60s.
	CoolDown time.Duration
}

func (c *ChannelConfig) defaults() {
	if c.MessagesPerSecond <= 0 {
		c.MessagesPerSecond = 5
	}
	if c.Burst <= 0 {
		c.Burst = 10
	}
	if c.CoolDown <= 0 {
		c.CoolDown = 60 * time.Second
	}
}

// nickState tracks per-nick rate limiting and escalation within a channel.
type nickState struct {
	tokens     float64
	lastRefill time.Time
	violations int
	lastAction time.Time
}

// channelState holds per-channel warden state.
type channelState struct {
	mu    sync.Mutex
	cfg   ChannelConfig
	nicks map[string]*nickState
}

func newChannelState(cfg ChannelConfig) *channelState {
	cfg.defaults()
	return &channelState{cfg: cfg, nicks: make(map[string]*nickState)}
}

// consume attempts to consume one token for nick. Returns true if allowed;
// false if rate-limited.
func (cs *channelState) consume(nick string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	ns, ok := cs.nicks[nick]
	if !ok {
		ns = &nickState{
			tokens:     float64(cs.cfg.Burst),
			lastRefill: time.Now(),
		}
		cs.nicks[nick] = ns
	}

	// Refill tokens based on elapsed time.
	now := time.Now()
	elapsed := now.Sub(ns.lastRefill).Seconds()
	ns.lastRefill = now
	ns.tokens = minF(float64(cs.cfg.Burst), ns.tokens+elapsed*cs.cfg.MessagesPerSecond)

	if ns.tokens >= 1 {
		ns.tokens--
		return true
	}
	return false
}

// violation records an enforcement action and returns the appropriate Action.
// Escalates: warn → mute → kick. Resets after CoolDown.
func (cs *channelState) violation(nick string) Action {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	ns, ok := cs.nicks[nick]
	if !ok {
		ns = &nickState{tokens: float64(cs.cfg.Burst), lastRefill: time.Now()}
		cs.nicks[nick] = ns
	}

	// Reset escalation after cool-down.
	if !ns.lastAction.IsZero() && time.Since(ns.lastAction) > cs.cfg.CoolDown {
		ns.violations = 0
	}

	ns.violations++
	ns.lastAction = time.Now()

	switch ns.violations {
	case 1:
		return ActionWarn
	case 2:
		return ActionMute
	default:
		return ActionKick
	}
}

// Bot is the warden.
type Bot struct {
	ircAddr        string
	password       string
	channelConfigs map[string]ChannelConfig // keyed by channel name
	defaultConfig  ChannelConfig
	mu             sync.RWMutex
	channels       map[string]*channelState
	log            *slog.Logger
	client         *girc.Client
}

// ActionRecord is written when warden takes action. Used in tests.
type ActionRecord struct {
	At      time.Time
	Channel string
	Nick    string
	Action  Action
	Reason  string
}

// ActionSink receives action records. Optional — if nil, actions are logged only.
type ActionSink interface {
	Record(ActionRecord)
}

// New creates a warden bot. channelConfigs overrides per-channel limits;
// defaultConfig is used for channels not in the map.
func New(ircAddr, password string, channelConfigs map[string]ChannelConfig, defaultConfig ChannelConfig, log *slog.Logger) *Bot {
	defaultConfig.defaults()
	return &Bot{
		ircAddr:        ircAddr,
		password:       password,
		channelConfigs: channelConfigs,
		defaultConfig:  defaultConfig,
		channels:       make(map[string]*channelState),
		log:            log,
	}
}

// Name returns the bot's IRC nick.
func (b *Bot) Name() string { return botNick }

// Start connects to IRC and begins moderation. Blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) error {
	host, port, err := splitHostPort(b.ircAddr)
	if err != nil {
		return fmt.Errorf("warden: parse irc addr: %w", err)
	}

	c := girc.New(girc.Config{
		Server: host,
		Port:   port,
		Nick:   botNick,
		User:   botNick,
		Name:   "scuttlebot warden",
		SASL:   &girc.SASLPlain{User: botNick, Pass: b.password},
		SSL:    false,
	})

	c.Handlers.AddBg(girc.CONNECTED, func(cl *girc.Client, _ girc.Event) {
		// Join all configured channels.
		for ch := range b.channelConfigs {
			cl.Cmd.Join(ch)
		}
		if b.log != nil {
			b.log.Info("warden connected")
		}
	})

	c.Handlers.AddBg(girc.INVITE, func(cl *girc.Client, e girc.Event) {
		if ch := e.Last(); strings.HasPrefix(ch, "#") {
			cl.Cmd.Join(ch)
		}
	})

	c.Handlers.AddBg(girc.PRIVMSG, func(cl *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil {
			return
		}
		channel := e.Params[0]
		if !strings.HasPrefix(channel, "#") {
			return
		}
		nick := e.Source.Name
		text := e.Last()

		cs := b.channelStateFor(channel)

		// Check for malformed envelope.
		if _, err := protocol.Unmarshal([]byte(text)); err != nil {
			// Non-JSON is human chat — not an error. Only warn if it looks like
			// a broken JSON attempt (starts with '{').
			if strings.HasPrefix(strings.TrimSpace(text), "{") {
				cl.Cmd.Notice(nick, "warden: malformed envelope ignored (invalid JSON)")
			}
			return
		}

		// Rate limit check.
		if !cs.consume(nick) {
			action := cs.violation(nick)
			b.enforce(cl, channel, nick, action, "rate limit exceeded")
		}
	})

	b.client = c

	errCh := make(chan error, 1)
	go func() {
		if err := c.Connect(); err != nil && ctx.Err() == nil {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		c.Close()
		return nil
	case err := <-errCh:
		return fmt.Errorf("warden: irc connection: %w", err)
	}
}

// Stop disconnects the bot.
func (b *Bot) Stop() {
	if b.client != nil {
		b.client.Close()
	}
}

func (b *Bot) channelStateFor(channel string) *channelState {
	b.mu.RLock()
	cs, ok := b.channels[channel]
	b.mu.RUnlock()
	if ok {
		return cs
	}

	cfg, ok := b.channelConfigs[channel]
	if !ok {
		cfg = b.defaultConfig
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	// Double-check after lock upgrade.
	if cs, ok = b.channels[channel]; ok {
		return cs
	}
	cs = newChannelState(cfg)
	b.channels[channel] = cs
	return cs
}

func (b *Bot) enforce(cl *girc.Client, channel, nick string, action Action, reason string) {
	if b.log != nil {
		b.log.Warn("warden: enforcing", "channel", channel, "nick", nick, "action", action, "reason", reason)
	}
	switch action {
	case ActionWarn:
		cl.Cmd.Notice(nick, fmt.Sprintf("warden: warning — %s in %s", reason, channel))
	case ActionMute:
		cl.Cmd.Notice(nick, fmt.Sprintf("warden: muted in %s — %s", channel, reason))
		cl.Cmd.Mode(channel, "+q", nick)
	case ActionKick:
		cl.Cmd.Kick(channel, nick, "warden: "+reason)
	}
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

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
