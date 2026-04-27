// Package steward implements the steward bot — a moderation action bot that
// watches for sentinel incident reports and takes proportional IRC action.
//
// Steward reads structured reports from the mod channel posted by sentinel
// (or any other source using the same format) and responds based on configured
// severity thresholds:
//
//   - low:    warn the user via NOTICE
//   - medium: warn + temporary mute (channel mode +q)
//   - high:   warn + kick (with reason)
//
// Every action steward takes is announced in the mod channel so the audit
// trail remains fully human-observable in IRC.
//
// Steward can also be commanded directly via DM by operators:
//
//	warn <nick> <#channel> <reason>
//	mute <nick> <#channel> [duration]
//	kick <nick> <#channel> <reason>
//	unmute <nick> <#channel>
package steward

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
)

const defaultNick = "steward"

// Config controls steward's behaviour.
type Config struct {
	// IRCAddr is host:port of the Ergo IRC server.
	IRCAddr string
	// Nick is the IRC nick. Default: "steward".
	Nick string
	// Password is the SASL PLAIN passphrase.
	Password string

	// ModChannel is the channel steward watches for sentinel reports and
	// where it announces its own actions. Default: "#moderation".
	ModChannel string

	// OperatorNicks is the list of nicks allowed to issue direct commands.
	OperatorNicks []string

	// AutoAct enables automatic action on sentinel reports.
	// When false, steward only acts on direct operator commands.
	AutoAct bool

	// MuteDuration is how long a medium-severity mute lasts. Default: 10m.
	MuteDuration time.Duration

	// SilenceLowWarnings, when true, suppresses warning notices for
	// low-severity incidents. Zero value (default) means low-severity
	// warnings are sent. Inverted from the original WarnOnLow flag because
	// a bool-with-default-true cannot be turned off via config — the
	// applyDefaults pass would force it back on. See #163.
	SilenceLowWarnings bool
	// DMOnAction, when true, sends a DM to all OperatorNicks when steward takes action.
	DMOnAction bool

	// CooldownPerNick is the minimum time between automated actions on the
	// same nick. Default: 5 minutes.
	CooldownPerNick time.Duration

	// Channels is the list of channels to join on connect.
	Channels []string
}

func (c *Config) setDefaults() {
	if c.Nick == "" {
		c.Nick = defaultNick
	}
	if c.ModChannel == "" {
		c.ModChannel = "#moderation"
	}
	if c.MuteDuration == 0 {
		c.MuteDuration = 10 * time.Minute
	}
	if c.CooldownPerNick == 0 {
		c.CooldownPerNick = 5 * time.Minute
	}
	// (No default needed for SilenceLowWarnings — Go's zero value (false)
	// already means "warn on low", which is the intended default. Operators
	// who want to suppress low-severity warnings set it to true.)
}

// Bot is the steward bot.
type Bot struct {
	cfg    Config
	log    *slog.Logger
	client *girc.Client

	mu       sync.Mutex
	cooldown map[string]time.Time // nick → last action time
	mutes    map[string]time.Time // "channel:nick" → unmute at
}

// New creates a steward Bot.
func New(cfg Config, log *slog.Logger) *Bot {
	cfg.setDefaults()
	return &Bot{
		cfg:      cfg,
		log:      log,
		cooldown: make(map[string]time.Time),
		mutes:    make(map[string]time.Time),
	}
}

// Start connects to IRC and begins watching for sentinel reports.
// Blocks until ctx is done.
func (b *Bot) Start(ctx context.Context) error {
	host, port, err := splitHostPort(b.cfg.IRCAddr)
	if err != nil {
		return fmt.Errorf("steward: %w", err)
	}

	c := girc.New(girc.Config{
		Server:      host,
		Port:        port,
		Nick:        b.cfg.Nick,
		User:        b.cfg.Nick,
		Name:        "scuttlebot steward",
		SASL:        &girc.SASLPlain{User: b.cfg.Nick, Pass: b.cfg.Password},
		PingDelay:   30 * time.Second,
		PingTimeout: 30 * time.Second,
	})

	c.Handlers.AddBg(girc.CONNECTED, func(cl *girc.Client, _ girc.Event) {
		cl.Cmd.Mode(cl.GetNick(), "+B")
		for _, ch := range b.cfg.Channels {
			cl.Cmd.Join(ch)
			// Steward enforces via mute (+b) and kick — both require +o.
			// Self-request OP via ChanServ AMODE so that a freshly-provisioned
			// channel grants it automatically on every future join. If
			// ChanServ refuses (e.g. channel not registered, or steward's
			// account lacks founder access), the AMODE silently fails and the
			// operator must grant manually — but the attempt makes intent
			// auditable. See #164.
			cl.Cmd.Message("ChanServ", fmt.Sprintf("AMODE %s +o %s", ch, b.cfg.Nick))
		}
		cl.Cmd.Join(b.cfg.ModChannel)
		cl.Cmd.Message("ChanServ", fmt.Sprintf("AMODE %s +o %s", b.cfg.ModChannel, b.cfg.Nick))
		if b.log != nil {
			b.log.Info("steward connected", "channels", b.cfg.Channels)
		}
	})

	c.Handlers.AddBg(girc.INVITE, func(cl *girc.Client, e girc.Event) {
		if ch := e.Last(); strings.HasPrefix(ch, "#") {
			cl.Cmd.Join(ch)
		}
	})

	router := cmdparse.NewRouter(b.cfg.Nick)
	router.SetPurpose("the moderation action bot — applies warn/mute/kick on sentinel reports or operator request")
	router.Register(cmdparse.Command{
		Name:        "warn",
		Usage:       "WARN <nick> [#channel] [reason]",
		Description: "warn a user via NOTICE",
		Handler: func(ctx *cmdparse.Context, args string) string {
			return b.cmdWarn(ctx, args)
		},
	})
	router.Register(cmdparse.Command{
		Name:        "mute",
		Usage:       "MUTE <nick> [#channel] [duration]",
		Description: "mute a user (default duration from config)",
		Handler: func(ctx *cmdparse.Context, args string) string {
			return b.cmdMute(ctx, args)
		},
	})
	router.Register(cmdparse.Command{
		Name:        "unmute",
		Usage:       "UNMUTE <nick> [#channel]",
		Description: "lift a mute early",
		Handler: func(ctx *cmdparse.Context, args string) string {
			return b.cmdUnmute(ctx, args)
		},
	})
	router.Register(cmdparse.Command{
		Name:        "kick",
		Usage:       "KICK <nick> [#channel] [reason]",
		Description: "kick a user from channel",
		Handler: func(ctx *cmdparse.Context, args string) string {
			return b.cmdKick(ctx, args)
		},
	})
	router.Register(cmdparse.Command{
		Name:        "status",
		Usage:       "STATUS",
		Description: "show mod channel, auto-act state, active mutes, and cooldowns",
		Handler: func(_ *cmdparse.Context, _ string) string {
			return b.cmdStatus()
		},
	})

	c.Handlers.AddBg(girc.PRIVMSG, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil {
			return
		}
		// Dispatch commands (DMs and channel messages).
		if reply := router.Dispatch(e.Source.Name, e.Params[0], e.Last()); reply != nil {
			for _, line := range reply.Lines() {
				c.Cmd.Message(reply.Target, line)
			}
			return
		}
		target := e.Params[0]
		nick := e.Source.Name
		text := strings.TrimSpace(e.Last())

		if nick == b.cfg.Nick {
			return
		}

		// Sentinel reports arrive as channel messages in the mod channel.
		if target == b.cfg.ModChannel && b.cfg.AutoAct {
			b.handleReport(c, text)
			return
		}

		// Direct operator commands arrive as DMs.
		if !strings.HasPrefix(target, "#") && b.isOperator(nick) {
			b.handleCommand(c, nick, text)
		}
	})

	b.mu.Lock()
	b.client = c
	b.mu.Unlock()

	// Background loop: unmute nicks whose mute duration has elapsed.
	go b.unmuteLoop(ctx)

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
		return fmt.Errorf("steward: irc: %w", err)
	}
}

// JoinChannel joins an additional channel (needed to set channel modes).
func (b *Bot) JoinChannel(channel string) {
	if b.client != nil {
		b.client.Cmd.Join(channel)
	}
}

// handleReport parses a sentinel incident report and takes action.
func (b *Bot) handleReport(c *girc.Client, text string) {
	if !strings.HasPrefix(text, "[sentinel]") {
		return
	}
	// [sentinel] incident in #channel | nick: X | severity: Y | reason: Z
	channel, nick, severity, reason := parseSentinelReport(text)
	if nick == "" || channel == "" {
		return
	}

	// Cooldown check.
	b.mu.Lock()
	if last, ok := b.cooldown[nick]; ok && time.Since(last) < b.cfg.CooldownPerNick {
		b.mu.Unlock()
		return
	}
	b.cooldown[nick] = time.Now()
	pruneTimeMap(b.cooldown, 24*time.Hour) // bound the map (#175)
	b.mu.Unlock()

	switch severity {
	case "high":
		b.kick(c, nick, channel, reason)
	case "medium":
		b.warn(c, nick, channel, reason)
		b.mute(c, nick, channel, b.cfg.MuteDuration)
	case "low":
		if !b.cfg.SilenceLowWarnings {
			b.warn(c, nick, channel, reason)
		}
	}
}

// handleCommand processes direct operator commands.
func (b *Bot) handleCommand(c *girc.Client, op, text string) {
	parts := strings.Fields(text)
	if len(parts) < 3 {
		c.Cmd.Notice(op, "steward: usage: warn|mute|kick|unmute <nick> <#channel> [reason/duration]")
		return
	}
	cmd, nick, channel := parts[0], parts[1], parts[2]
	rest := strings.Join(parts[3:], " ")

	switch strings.ToLower(cmd) {
	case "warn":
		reason := rest
		if reason == "" {
			reason = "operator warning"
		}
		b.warn(c, nick, channel, reason)
	case "mute":
		d := b.cfg.MuteDuration
		if rest != "" {
			if parsed, err := time.ParseDuration(rest); err == nil {
				d = parsed
			}
		}
		b.mute(c, nick, channel, d)
	case "kick":
		reason := rest
		if reason == "" {
			reason = "removed by steward"
		}
		b.kick(c, nick, channel, reason)
	case "unmute":
		b.unmute(c, nick, channel)
	default:
		c.Cmd.Notice(op, fmt.Sprintf("steward: unknown command %q", cmd))
	}
}

func (b *Bot) warn(c *girc.Client, nick, channel, reason string) {
	c.Cmd.Notice(nick, fmt.Sprintf("[steward] warning in %s: %s", channel, reason))
	b.announce(c, fmt.Sprintf("warned %s in %s — %s", nick, channel, reason))
	if b.log != nil {
		b.log.Info("steward warn", "nick", nick, "channel", channel, "reason", reason)
	}
}

func (b *Bot) mute(c *girc.Client, nick, channel string, d time.Duration) {
	// Extended ban m: to mute — agent stays in channel but cannot speak.
	c.Cmd.Mode(channel, "+b", "m:"+nick+"!*@*")
	key := channel + ":" + nick
	b.mu.Lock()
	b.mutes[key] = time.Now().Add(d)
	b.mu.Unlock()
	b.announce(c, fmt.Sprintf("muted %s in %s for %s", nick, channel, d.Round(time.Second)))
	if b.log != nil {
		b.log.Info("steward mute", "nick", nick, "channel", channel, "duration", d)
	}
}

func (b *Bot) unmute(c *girc.Client, nick, channel string) {
	c.Cmd.Mode(channel, "-b", "m:"+nick+"!*@*")
	key := channel + ":" + nick
	b.mu.Lock()
	delete(b.mutes, key)
	b.mu.Unlock()
	b.announce(c, fmt.Sprintf("unmuted %s in %s", nick, channel))
	if b.log != nil {
		b.log.Info("steward unmute", "nick", nick, "channel", channel)
	}
}

func (b *Bot) kick(c *girc.Client, nick, channel, reason string) {
	c.Cmd.Kick(channel, nick, reason)
	b.announce(c, fmt.Sprintf("kicked %s from %s — %s", nick, channel, reason))
	if b.log != nil {
		b.log.Info("steward kick", "nick", nick, "channel", channel, "reason", reason)
	}
}

func (b *Bot) announce(c *girc.Client, msg string) {
	full := "[steward] " + msg
	c.Cmd.Message(b.cfg.ModChannel, full)
	if b.cfg.DMOnAction {
		for _, op := range b.cfg.OperatorNicks {
			c.Cmd.Message(op, full)
		}
	}
}

// unmuteLoop lifts expired mutes.
func (b *Bot) unmuteLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			b.mu.Lock()
			expired := make(map[string]time.Time)
			for key, at := range b.mutes {
				if now.After(at) {
					expired[key] = at
					delete(b.mutes, key)
				}
			}
			b.mu.Unlock()
			for key := range expired {
				parts := strings.SplitN(key, ":", 2)
				if len(parts) != 2 {
					continue
				}
				channel, nick := parts[0], parts[1]
				if b.client != nil {
					b.unmute(b.client, nick, channel)
				}
			}
		}
	}
}

func (b *Bot) isOperator(nick string) bool {
	for _, op := range b.cfg.OperatorNicks {
		if strings.EqualFold(op, nick) {
			return true
		}
	}
	return false
}

// parseSentinelReport parses:
// [sentinel] incident in #channel | nick: X | severity: Y | reason: Z
func parseSentinelReport(text string) (channel, nick, severity, reason string) {
	// Strip prefix up to "incident in"
	idx := strings.Index(strings.ToLower(text), "incident in")
	if idx == -1 {
		return
	}
	rest := text[idx+len("incident in"):]
	parts := strings.Split(rest, "|")
	if len(parts) < 1 {
		return
	}
	channel = strings.TrimSpace(parts[0])
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if kv, ok := strings.CutPrefix(p, "nick:"); ok {
			nick = strings.TrimSpace(kv)
		} else if kv, ok := strings.CutPrefix(p, "severity:"); ok {
			severity = strings.ToLower(strings.TrimSpace(kv))
		} else if kv, ok := strings.CutPrefix(p, "reason:"); ok {
			reason = strings.TrimSpace(kv)
		}
	}
	return
}

// pruneTimeMap drops keys whose timestamp is older than maxAge. Keeps the
// cooldown map bounded over long-running deployments (#175).
// Caller must hold the appropriate lock.
func pruneTimeMap(m map[string]time.Time, maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	for k, t := range m {
		if t.Before(cutoff) {
			delete(m, k)
		}
	}
}

// cmdWarn / cmdMute / cmdUnmute / cmdKick / cmdStatus implement the cmdparse
// commands. Authorisation: the invoker must either be in OperatorNicks
// (privileged direct command) or already hold +o in the target channel
// (ad-hoc moderation by a channel op).

func (b *Bot) cmdWarn(ctx *cmdparse.Context, args string) string {
	target, channel, rest, err := b.parseModArgs(ctx, args)
	if err != nil {
		return err.Error()
	}
	if !b.authzMod(ctx.Nick, channel) {
		return "steward: only operators or channel ops may issue WARN"
	}
	reason := strings.TrimSpace(rest)
	if reason == "" {
		reason = fmt.Sprintf("operator warning by %s", ctx.Nick)
	}
	b.warn(b.client, target, channel, reason)
	return fmt.Sprintf("steward: warned %s in %s", target, channel)
}

func (b *Bot) cmdMute(ctx *cmdparse.Context, args string) string {
	target, channel, rest, err := b.parseModArgs(ctx, args)
	if err != nil {
		return err.Error()
	}
	if !b.authzMod(ctx.Nick, channel) {
		return "steward: only operators or channel ops may issue MUTE"
	}
	d := b.cfg.MuteDuration
	if rest = strings.TrimSpace(rest); rest != "" {
		if parsed, perr := time.ParseDuration(rest); perr == nil {
			d = parsed
		} else {
			return fmt.Sprintf("steward: bad duration %q — use go time.ParseDuration syntax (e.g. 5m, 1h)", rest)
		}
	}
	b.mute(b.client, target, channel, d)
	return fmt.Sprintf("steward: muted %s in %s for %s", target, channel, d.Round(time.Second))
}

func (b *Bot) cmdUnmute(ctx *cmdparse.Context, args string) string {
	target, channel, _, err := b.parseModArgs(ctx, args)
	if err != nil {
		return err.Error()
	}
	if !b.authzMod(ctx.Nick, channel) {
		return "steward: only operators or channel ops may issue UNMUTE"
	}
	b.unmute(b.client, target, channel)
	return fmt.Sprintf("steward: unmuted %s in %s", target, channel)
}

func (b *Bot) cmdKick(ctx *cmdparse.Context, args string) string {
	target, channel, rest, err := b.parseModArgs(ctx, args)
	if err != nil {
		return err.Error()
	}
	if !b.authzMod(ctx.Nick, channel) {
		return "steward: only operators or channel ops may issue KICK"
	}
	reason := strings.TrimSpace(rest)
	if reason == "" {
		reason = fmt.Sprintf("removed by %s", ctx.Nick)
	}
	b.kick(b.client, target, channel, reason)
	return fmt.Sprintf("steward: kicked %s from %s", target, channel)
}

func (b *Bot) cmdStatus() string {
	b.mu.Lock()
	muteCount := len(b.mutes)
	cooldownCount := len(b.cooldown)
	b.mu.Unlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("steward: mod channel %s | ", b.cfg.ModChannel))
	if b.cfg.AutoAct {
		sb.WriteString("auto-act ON")
	} else {
		sb.WriteString("auto-act OFF (operator-only)")
	}
	sb.WriteString(fmt.Sprintf(" | mute duration %s | cooldown/nick %s\n",
		b.cfg.MuteDuration.Round(time.Second), b.cfg.CooldownPerNick.Round(time.Second)))
	sb.WriteString(fmt.Sprintf("active mutes: %d | nicks in cooldown: %d", muteCount, cooldownCount))
	return sb.String()
}

func (b *Bot) parseModArgs(ctx *cmdparse.Context, args string) (nick, channel, rest string, err error) {
	args = strings.TrimSpace(args)
	if args == "" {
		return "", "", "", fmt.Errorf("steward: usage — <nick> [#channel] [reason/duration]")
	}
	fields := strings.Fields(args)
	nick = fields[0]
	tail := fields[1:]
	channel = ctx.Channel
	if len(tail) > 0 && strings.HasPrefix(tail[0], "#") {
		channel = tail[0]
		tail = tail[1:]
	}
	if channel == "" || channel == b.cfg.ModChannel {
		// Mod channel itself is steward's broadcast channel — don't moderate
		// there. If the operator addressed steward in #moderation without
		// naming a channel, that's almost certainly an error.
		if channel == b.cfg.ModChannel {
			return "", "", "", fmt.Errorf("steward: name a target #channel — %s is steward's broadcast channel", b.cfg.ModChannel)
		}
		return "", "", "", fmt.Errorf("steward: in a DM you must name a #channel")
	}
	rest = strings.Join(tail, " ")
	return nick, channel, rest, nil
}

// authzMod gates moderation commands. The invoker is allowed if they are
// listed in OperatorNicks (configured trusted operators) or already hold +o
// in the target channel.
func (b *Bot) authzMod(nick, channel string) bool {
	if b.isOperator(nick) {
		return true
	}
	if b.client == nil {
		return false
	}
	user := b.client.LookupUser(nick)
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
