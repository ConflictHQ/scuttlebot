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

	"github.com/conflicthq/scuttlebot/internal/bots/cmdparse"
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
	// Loop detection: track recent messages for repetition.
	recentMsgs []string
}

// channelMsg is a recent message for ping-pong detection.
type channelMsg struct {
	nick string
	text string
}

// channelState holds per-channel warden state.
type channelState struct {
	mu         sync.Mutex
	cfg        ChannelConfig
	nicks      map[string]*nickState
	recentMsgs []channelMsg // channel-wide message history for ping-pong detection
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

// recordMessage tracks a message for loop detection. Returns true if a loop
// is detected (same message repeated 3+ times in recent history).
func (cs *channelState) recordMessage(nick, text string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	ns, ok := cs.nicks[nick]
	if !ok {
		ns = &nickState{tokens: float64(cs.cfg.Burst), lastRefill: time.Now()}
		cs.nicks[nick] = ns
	}

	ns.recentMsgs = append(ns.recentMsgs, text)
	// Keep last 10 messages.
	if len(ns.recentMsgs) > 10 {
		ns.recentMsgs = ns.recentMsgs[len(ns.recentMsgs)-10:]
	}

	// Check for repetition: same message 3+ times in last 10.
	count := 0
	for _, m := range ns.recentMsgs {
		if m == text {
			count++
		}
	}
	return count >= 3
}

// recordChannelMessage tracks messages at channel level for ping-pong detection.
// Returns the offending nick if a ping-pong loop is detected (two nicks
// alternating back and forth 4+ times with no other participants).
func (cs *channelState) recordChannelMessage(nick, text string) string {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cs.recentMsgs = append(cs.recentMsgs, channelMsg{nick: nick, text: text})
	if len(cs.recentMsgs) > 20 {
		cs.recentMsgs = cs.recentMsgs[len(cs.recentMsgs)-20:]
	}

	// Check last 8 messages for A-B-A-B pattern.
	msgs := cs.recentMsgs
	if len(msgs) < 8 {
		return ""
	}
	tail := msgs[len(msgs)-8:]
	nickA := tail[0].nick
	nickB := tail[1].nick
	if nickA == nickB {
		return ""
	}
	for i, m := range tail {
		expected := nickA
		if i%2 == 1 {
			expected = nickB
		}
		if m.nick != expected {
			return ""
		}
	}
	// A-B-A-B-A-B-A-B pattern detected — return the most recent speaker.
	return nick
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
	initChannels   []string                 // channels to join on connect
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
func New(ircAddr, password string, channels []string, channelConfigs map[string]ChannelConfig, defaultConfig ChannelConfig, log *slog.Logger) *Bot {
	defaultConfig.defaults()
	return &Bot{
		ircAddr:        ircAddr,
		password:       password,
		initChannels:   channels,
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
		Server:      host,
		Port:        port,
		Nick:        botNick,
		User:        botNick,
		Name:        "scuttlebot warden",
		SASL:        &girc.SASLPlain{User: botNick, Pass: b.password},
		PingDelay:   30 * time.Second,
		PingTimeout: 30 * time.Second,
		SSL:         false,
	})

	c.Handlers.AddBg(girc.CONNECTED, func(cl *girc.Client, _ girc.Event) {
		cl.Cmd.Mode(cl.GetNick(), "+B")
		// Warden enforces via mute (+b nick!*@*) and kick — both require +o.
		// Self-request OP via ChanServ AMODE so a freshly-provisioned channel
		// grants it on every future join. If ChanServ refuses (e.g. channel
		// not registered) the AMODE silently fails and the operator must
		// grant manually. See #164.
		joined := make(map[string]struct{})
		for _, ch := range b.initChannels {
			cl.Cmd.Join(ch)
			joined[ch] = struct{}{}
			cl.Cmd.Message("ChanServ", fmt.Sprintf("AMODE %s +o %s", ch, botNick))
		}
		for ch := range b.channelConfigs {
			if _, dup := joined[ch]; dup {
				continue
			}
			cl.Cmd.Join(ch)
			cl.Cmd.Message("ChanServ", fmt.Sprintf("AMODE %s +o %s", ch, botNick))
		}
		if b.log != nil {
			b.log.Info("warden connected", "channels", b.initChannels)
		}
	})

	c.Handlers.AddBg(girc.INVITE, func(cl *girc.Client, e girc.Event) {
		if ch := e.Last(); strings.HasPrefix(ch, "#") {
			cl.Cmd.Join(ch)
		}
	})

	router := cmdparse.NewRouter(botNick)
	router.Register(cmdparse.Command{
		Name:        "warn",
		Usage:       "WARN <nick> [reason]",
		Description: "issue a warning to a user",
		Handler:     func(_ *cmdparse.Context, _ string) string { return "not implemented yet" },
	})
	router.Register(cmdparse.Command{
		Name:        "mute",
		Usage:       "MUTE <nick> [duration]",
		Description: "mute a user",
		Handler:     func(_ *cmdparse.Context, _ string) string { return "not implemented yet" },
	})
	router.Register(cmdparse.Command{
		Name:        "kick",
		Usage:       "KICK <nick> [reason]",
		Description: "kick a user from channel",
		Handler:     func(_ *cmdparse.Context, _ string) string { return "not implemented yet" },
	})
	router.Register(cmdparse.Command{
		Name:        "status",
		Usage:       "STATUS",
		Description: "show current warnings and mutes",
		Handler:     func(_ *cmdparse.Context, _ string) string { return "not implemented yet" },
	})

	c.Handlers.AddBg(girc.PRIVMSG, func(cl *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil {
			return
		}
		// Dispatch commands (DMs and channel messages).
		if reply := router.Dispatch(e.Source.Name, e.Params[0], e.Last()); reply != nil {
			cl.Cmd.Message(reply.Target, reply.Text)
			return
		}
		channel := e.Params[0]
		if !strings.HasPrefix(channel, "#") {
			return // non-command DMs ignored
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

		// Skip enforcement for channel ops (+o and above).
		if isChannelOp(cl, channel, nick) {
			return
		}

		// Loop detection: same message repeated 3+ times → mute.
		if cs.recordMessage(nick, text) {
			b.enforce(cl, channel, nick, ActionMute, "repetitive message loop detected")
			return
		}

		// Ping-pong detection: two agents alternating back and forth → mute the latest.
		if loopNick := cs.recordChannelMessage(nick, text); loopNick != "" {
			b.enforce(cl, channel, loopNick, ActionMute, "agent ping-pong loop detected")
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
		// Use extended ban m: to mute — agent stays in channel but cannot speak.
		mask := "m:" + nick + "!*@*"
		cl.Cmd.Mode(channel, "+b", mask)
		// Remove mute after cooldown so the agent can recover.
		cs := b.channelStateFor(channel)
		go func() {
			time.Sleep(cs.cfg.CoolDown)
			cl.Cmd.Mode(channel, "-b", mask)
		}()
	case ActionKick:
		cl.Cmd.Kick(channel, nick, "warden: "+reason)
	}
}

// isChannelOp returns true if nick has +o or higher in the given channel.
// Returns false if the user or channel cannot be looked up (e.g. not tracked).
func isChannelOp(cl *girc.Client, channel, nick string) bool {
	user := cl.LookupUser(nick)
	if user == nil || user.Perms == nil {
		return false
	}
	perms, ok := user.Perms.Lookup(channel)
	if !ok {
		return false
	}
	return perms.IsAdmin()
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
