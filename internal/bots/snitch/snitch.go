// Package snitch implements a surveillance bot that watches for erratic
// behaviour across IRC channels and alerts operators via DM or a
// dedicated alert channel.
//
// Detected conditions:
//   - Message flooding (burst above threshold in a rolling window)
//   - Rapid join/part cycling
//   - Repeated malformed / non-JSON messages from registered agents
package snitch

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

const defaultNick = "snitch"

// Config controls snitch's thresholds and alert destination.
type Config struct {
	// IRCAddr is host:port of the Ergo IRC server.
	IRCAddr string
	// Nick is the IRC nick for the bot. Default: "snitch".
	Nick string
	// Password is the SASL PLAIN passphrase for the bot's NickServ account.
	Password string

	// AlertChannel is the channel to post alerts to (e.g. "#ops").
	// If empty, alerts are sent only as DMs to AlertNicks.
	AlertChannel string
	// AlertNicks is the list of operator nicks to DM on an alert.
	AlertNicks []string

	// FloodMessages is the number of messages in FloodWindow that triggers
	// a flood alert. Default: 10.
	FloodMessages int
	// FloodWindow is the rolling window for flood detection. Default: 5s.
	FloodWindow time.Duration
	// JoinPartThreshold is join+part events in JoinPartWindow to trigger alert. Default: 5.
	JoinPartThreshold int
	// JoinPartWindow is the rolling window for join/part cycling. Default: 30s.
	JoinPartWindow time.Duration

	// Channels is the list of channels to join on connect.
	Channels []string

	// MonitorNicks is the list of nicks to track via IRC MONITOR.
	// Snitch will alert when a monitored nick goes offline unexpectedly.
	MonitorNicks []string
}

func (c *Config) setDefaults() {
	if c.Nick == "" {
		c.Nick = defaultNick
	}
	if c.FloodMessages == 0 {
		c.FloodMessages = 10
	}
	if c.FloodWindow == 0 {
		c.FloodWindow = 5 * time.Second
	}
	if c.JoinPartThreshold == 0 {
		c.JoinPartThreshold = 5
	}
	if c.JoinPartWindow == 0 {
		c.JoinPartWindow = 30 * time.Second
	}
}

// nickWindow tracks event timestamps for a single nick in a single channel.
type nickWindow struct {
	msgs     []time.Time
	joinPart []time.Time
}

func (nw *nickWindow) trim(now time.Time, msgWindow, jpWindow time.Duration) {
	cutMsg := now.Add(-msgWindow)
	filtered := nw.msgs[:0]
	for _, t := range nw.msgs {
		if t.After(cutMsg) {
			filtered = append(filtered, t)
		}
	}
	nw.msgs = filtered

	cutJP := now.Add(-jpWindow)
	filteredJP := nw.joinPart[:0]
	for _, t := range nw.joinPart {
		if t.After(cutJP) {
			filteredJP = append(filteredJP, t)
		}
	}
	nw.joinPart = filteredJP
}

// Bot is the snitch bot.
type Bot struct {
	cfg    Config
	log    *slog.Logger
	client *girc.Client

	mu      sync.Mutex
	windows map[string]map[string]*nickWindow // channel → nick → window
	alerted map[string]time.Time              // key → last alert time (cooldown)
}

// New creates a snitch Bot.
func New(cfg Config, log *slog.Logger) *Bot {
	cfg.setDefaults()
	return &Bot{
		cfg:     cfg,
		log:     log,
		windows: make(map[string]map[string]*nickWindow),
		alerted: make(map[string]time.Time),
	}
}

// Start connects to IRC and begins surveillance. Blocks until ctx is done.
func (b *Bot) Start(ctx context.Context) error {
	host, port, err := splitHostPort(b.cfg.IRCAddr)
	if err != nil {
		return fmt.Errorf("snitch: %w", err)
	}

	c := girc.New(girc.Config{
		Server:      host,
		Port:        port,
		Nick:        b.cfg.Nick,
		User:        b.cfg.Nick,
		Name:        "scuttlebot snitch",
		SASL:        &girc.SASLPlain{User: b.cfg.Nick, Pass: b.cfg.Password},
		PingDelay:   30 * time.Second,
		PingTimeout: 30 * time.Second,
	})

	c.Handlers.AddBg(girc.CONNECTED, func(cl *girc.Client, _ girc.Event) {
		cl.Cmd.Mode(cl.GetNick(), "+B")
		for _, ch := range b.cfg.Channels {
			cl.Cmd.Join(ch)
		}
		if b.cfg.AlertChannel != "" {
			cl.Cmd.Join(b.cfg.AlertChannel)
		}
		if len(b.cfg.MonitorNicks) > 0 {
			_ = cl.Cmd.SendRawf("MONITOR + %s", strings.Join(b.cfg.MonitorNicks, ","))
		}
		if b.log != nil {
			b.log.Info("snitch connected", "channels", b.cfg.Channels, "monitor", b.cfg.MonitorNicks)
		}
	})

	// away-notify: track agents going idle or returning.
	c.Handlers.AddBg(girc.AWAY, func(_ *girc.Client, e girc.Event) {
		if e.Source == nil {
			return
		}
		nick := e.Source.Name
		reason := e.Last()
		if reason != "" {
			b.alert(fmt.Sprintf("agent away: %s (%s)", nick, reason))
		}
	})

	c.Handlers.AddBg(girc.RPL_MONOFFLINE, func(_ *girc.Client, e girc.Event) {
		nicks := e.Last()
		for _, nick := range strings.Split(nicks, ",") {
			nick = strings.TrimSpace(nick)
			if nick == "" {
				continue
			}
			b.alert(fmt.Sprintf("monitored nick offline: %s", nick))
		}
	})

	c.Handlers.AddBg(girc.INVITE, func(cl *girc.Client, e girc.Event) {
		if ch := e.Last(); strings.HasPrefix(ch, "#") {
			cl.Cmd.Join(ch)
		}
	})

	c.Handlers.AddBg(girc.JOIN, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil || e.Source.Name == b.cfg.Nick {
			return
		}
		b.recordJoinPart(e.Params[0], e.Source.Name)
	})

	c.Handlers.AddBg(girc.PART, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil {
			return
		}
		b.recordJoinPart(e.Params[0], e.Source.Name)
	})

	router := cmdparse.NewRouter(b.cfg.Nick)
	router.Register(cmdparse.Command{
		Name:        "status",
		Usage:       "STATUS",
		Description: "show current active alerts",
		Handler:     func(_ *cmdparse.Context, _ string) string { return "not implemented yet" },
	})
	router.Register(cmdparse.Command{
		Name:        "acknowledge",
		Usage:       "ACKNOWLEDGE <alert-id>",
		Description: "acknowledge an alert",
		Handler:     func(_ *cmdparse.Context, _ string) string { return "not implemented yet" },
	})

	c.Handlers.AddBg(girc.PRIVMSG, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil {
			return
		}
		// Dispatch commands (DMs and channel messages).
		if reply := router.Dispatch(e.Source.Name, e.Params[0], e.Last()); reply != nil {
			c.Cmd.Message(reply.Target, reply.Text)
			return
		}
		channel := e.Params[0]
		nick := e.Source.Name
		if nick == b.cfg.Nick {
			return
		}
		b.recordMsg(channel, nick)
		b.checkFlood(c, channel, nick)
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

	select {
	case <-ctx.Done():
		c.Close()
		return nil
	case err := <-errCh:
		return fmt.Errorf("snitch: irc: %w", err)
	}
}

func (b *Bot) JoinChannel(channel string) {
	if b.client != nil {
		b.client.Cmd.Join(channel)
	}
}

// MonitorAdd adds nicks to the MONITOR list at runtime.
func (b *Bot) MonitorAdd(nicks ...string) {
	if b.client != nil && len(nicks) > 0 {
		_ = b.client.Cmd.SendRawf("MONITOR + %s", strings.Join(nicks, ","))
	}
}

// MonitorRemove removes nicks from the MONITOR list at runtime.
func (b *Bot) MonitorRemove(nicks ...string) {
	if b.client != nil && len(nicks) > 0 {
		_ = b.client.Cmd.SendRawf("MONITOR - %s", strings.Join(nicks, ","))
	}
}

func (b *Bot) window(channel, nick string) *nickWindow {
	if b.windows[channel] == nil {
		b.windows[channel] = make(map[string]*nickWindow)
	}
	if b.windows[channel][nick] == nil {
		b.windows[channel][nick] = &nickWindow{}
	}
	return b.windows[channel][nick]
}

func (b *Bot) recordMsg(channel, nick string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	w := b.window(channel, nick)
	w.trim(now, b.cfg.FloodWindow, b.cfg.JoinPartWindow)
	w.msgs = append(w.msgs, now)
}

func (b *Bot) recordJoinPart(channel, nick string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	w := b.window(channel, nick)
	w.trim(now, b.cfg.FloodWindow, b.cfg.JoinPartWindow)
	w.joinPart = append(w.joinPart, now)
	if len(w.joinPart) >= b.cfg.JoinPartThreshold {
		go b.alert(fmt.Sprintf("join/part cycling: %s in %s (%d events in %s)",
			nick, channel, len(w.joinPart), b.cfg.JoinPartWindow))
		w.joinPart = nil
	}
}

func (b *Bot) checkFlood(c *girc.Client, channel, nick string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	w := b.window(channel, nick)
	w.trim(now, b.cfg.FloodWindow, b.cfg.JoinPartWindow)
	if len(w.msgs) >= b.cfg.FloodMessages {
		key := "flood:" + channel + ":" + nick
		if last, ok := b.alerted[key]; !ok || now.Sub(last) > 60*time.Second {
			b.alerted[key] = now
			pruneTimeMap(b.alerted, 24*time.Hour) // bound the map (#175)
			go b.alert(fmt.Sprintf("flood detected: %s in %s (%d msgs in %s)",
				nick, channel, len(w.msgs), b.cfg.FloodWindow))
		}
	}
}

func (b *Bot) alert(msg string) {
	if b.client == nil {
		return
	}
	if b.log != nil {
		b.log.Warn("snitch alert", "msg", msg)
	}
	full := "[snitch] " + msg
	if b.cfg.AlertChannel != "" {
		b.client.Cmd.Message(b.cfg.AlertChannel, full)
	}
	for _, nick := range b.cfg.AlertNicks {
		b.client.Cmd.Message(nick, full)
	}
}

// pruneTimeMap drops keys whose timestamp is older than maxAge. Keeps the
// alerted/cooldown map bounded over long-running deployments (#175).
// Caller must hold the appropriate lock.
func pruneTimeMap(m map[string]time.Time, maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	for k, t := range m {
		if t.Before(cutoff) {
			delete(m, k)
		}
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
