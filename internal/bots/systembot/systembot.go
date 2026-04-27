// Package systembot implements the systembot — IRC system event logger.
//
// systembot is the complement to scribe: where scribe owns the agent message
// stream (PRIVMSG), systembot owns the system stream:
//   - NOTICE messages (server announcements, NickServ/ChanServ responses)
//   - Connection events: JOIN, PART, QUIT, KICK
//   - Mode changes: MODE
//
// Every event is written to a Store as a SystemEntry.
package systembot

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

const botNick = "systembot"

// EntryKind classifies a system event.
type EntryKind string

const (
	KindNotice EntryKind = "notice"
	KindJoin   EntryKind = "join"
	KindPart   EntryKind = "part"
	KindQuit   EntryKind = "quit"
	KindKick   EntryKind = "kick"
	KindMode   EntryKind = "mode"
)

// Entry is a single system event log record.
type Entry struct {
	At      time.Time
	Kind    EntryKind
	Channel string // empty for server-level events (QUIT, server NOTICE)
	Nick    string // who triggered the event; empty for server events
	Text    string // message text, mode string, kick reason, etc.
}

// Store is where system entries are written.
type Store interface {
	Append(Entry) error
}

// Bot is the systembot.
type Bot struct {
	ircAddr  string
	password string
	channels []string
	store    Store
	log      *slog.Logger
	client   *girc.Client

	mu        sync.Mutex
	recent    []Entry // ring buffer for STATUS / RECENT command output
	startedAt time.Time
}

const maxRecentSystem = 100

// New creates a systembot.
func New(ircAddr, password string, channels []string, store Store, log *slog.Logger) *Bot {
	return &Bot{
		ircAddr:   ircAddr,
		password:  password,
		channels:  channels,
		store:     store,
		log:       log,
		startedAt: time.Now(),
	}
}

// Name returns the bot's IRC nick.
func (b *Bot) Name() string { return botNick }

// Start connects to IRC and begins logging system events. Blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) error {
	host, port, err := splitHostPort(b.ircAddr)
	if err != nil {
		return fmt.Errorf("systembot: parse irc addr: %w", err)
	}

	c := girc.New(girc.Config{
		Server:      host,
		Port:        port,
		Nick:        botNick,
		User:        botNick,
		Name:        "scuttlebot systembot",
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
		b.log.Info("systembot connected", "channels", b.channels)
	})

	c.Handlers.AddBg(girc.INVITE, func(cl *girc.Client, e girc.Event) {
		if ch := e.Last(); strings.HasPrefix(ch, "#") {
			cl.Cmd.Join(ch)
		}
	})

	// NOTICE — server announcements, NickServ/ChanServ responses.
	c.Handlers.AddBg(girc.NOTICE, func(_ *girc.Client, e girc.Event) {
		channel := ""
		if len(e.Params) > 0 && strings.HasPrefix(e.Params[0], "#") {
			channel = e.Params[0]
		}
		nick := ""
		if e.Source != nil {
			nick = e.Source.Name
		}
		b.write(Entry{Kind: KindNotice, Channel: channel, Nick: nick, Text: e.Last()})
	})

	// JOIN
	c.Handlers.AddBg(girc.JOIN, func(_ *girc.Client, e girc.Event) {
		channel := e.Last()
		if len(e.Params) > 0 {
			channel = e.Params[0]
		}
		nick := ""
		if e.Source != nil {
			nick = e.Source.Name
		}
		b.write(Entry{Kind: KindJoin, Channel: channel, Nick: nick})
	})

	// PART
	c.Handlers.AddBg(girc.PART, func(_ *girc.Client, e girc.Event) {
		channel := ""
		if len(e.Params) > 0 {
			channel = e.Params[0]
		}
		nick := ""
		if e.Source != nil {
			nick = e.Source.Name
		}
		b.write(Entry{Kind: KindPart, Channel: channel, Nick: nick, Text: e.Last()})
	})

	// QUIT
	c.Handlers.AddBg(girc.QUIT, func(_ *girc.Client, e girc.Event) {
		nick := ""
		if e.Source != nil {
			nick = e.Source.Name
		}
		b.write(Entry{Kind: KindQuit, Nick: nick, Text: e.Last()})
	})

	// KICK
	c.Handlers.AddBg(girc.KICK, func(_ *girc.Client, e girc.Event) {
		channel := ""
		if len(e.Params) > 0 {
			channel = e.Params[0]
		}
		kicked := ""
		if len(e.Params) > 1 {
			kicked = e.Params[1]
		}
		b.write(Entry{Kind: KindKick, Channel: channel, Nick: kicked, Text: e.Last()})
	})

	// MODE
	c.Handlers.AddBg(girc.MODE, func(_ *girc.Client, e girc.Event) {
		channel := ""
		if len(e.Params) > 0 && strings.HasPrefix(e.Params[0], "#") {
			channel = e.Params[0]
		}
		nick := ""
		if e.Source != nil {
			nick = e.Source.Name
		}
		b.write(Entry{Kind: KindMode, Channel: channel, Nick: nick, Text: strings.Join(e.Params, " ")})
	})

	router := cmdparse.NewRouter(botNick)
	router.SetPurpose("the system event logger — records joins/parts/quits/kicks/modes/notices")
	router.Register(cmdparse.Command{
		Name:        "status",
		Usage:       "STATUS",
		Description: "show uptime, connected channels, and recent system event counts",
		Handler: func(_ *cmdparse.Context, _ string) string {
			return b.cmdStatus(c)
		},
	})
	router.Register(cmdparse.Command{
		Name:        "who",
		Usage:       "WHO [#channel]",
		Description: "list users in a channel",
		Handler: func(ctx *cmdparse.Context, args string) string {
			return b.cmdWho(c, ctx, args)
		},
	})
	router.Register(cmdparse.Command{
		Name:        "recent",
		Usage:       "RECENT [N]",
		Description: "show the last N system events (default 10, max 50)",
		Handler: func(_ *cmdparse.Context, args string) string {
			return b.cmdRecent(args)
		},
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
		return fmt.Errorf("systembot: irc connection: %w", err)
	}
}

// Stop disconnects the bot.
func (b *Bot) Stop() {
	if b.client != nil {
		b.client.Close()
	}
}

func (b *Bot) write(e Entry) {
	e.At = time.Now()
	if err := b.store.Append(e); err != nil {
		b.log.Error("systembot: failed to write entry", "kind", e.Kind, "err", err)
	}
	b.mu.Lock()
	b.recent = append(b.recent, e)
	if len(b.recent) > maxRecentSystem {
		b.recent = b.recent[len(b.recent)-maxRecentSystem:]
	}
	b.mu.Unlock()
}

func (b *Bot) cmdStatus(c *girc.Client) string {
	b.mu.Lock()
	counts := make(map[EntryKind]int)
	for _, e := range b.recent {
		counts[e.Kind]++
	}
	total := len(b.recent)
	b.mu.Unlock()

	channels := []string{}
	if c != nil && c.IsConnected() {
		channels = c.ChannelList()
		sort.Strings(channels)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("systembot: uptime %s | joined %d channel(s)\n",
		time.Since(b.startedAt).Round(time.Second), len(channels)))
	if len(channels) > 0 {
		sb.WriteString("channels:")
		for _, ch := range channels {
			sb.WriteString(" " + ch)
		}
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("recent events (%d in window):", total))
	for _, kind := range []EntryKind{KindJoin, KindPart, KindQuit, KindKick, KindMode, KindNotice} {
		if counts[kind] > 0 {
			sb.WriteString(fmt.Sprintf(" %s=%d", kind, counts[kind]))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (b *Bot) cmdWho(c *girc.Client, ctx *cmdparse.Context, args string) string {
	channel := strings.TrimSpace(args)
	if channel == "" {
		channel = ctx.Channel
	}
	if channel == "" {
		return "systembot: usage — WHO [#channel] (must name a channel in DMs)"
	}
	if c == nil {
		return "systembot: not connected"
	}
	ch := c.LookupChannel(channel)
	if ch == nil {
		return fmt.Sprintf("systembot: not in %s — invite me with /invite %s", channel, botNick)
	}
	users := ch.UserList
	sort.Strings(users)

	if len(users) == 0 {
		return fmt.Sprintf("systembot: %s — no users tracked", channel)
	}
	if len(users) > 60 {
		return fmt.Sprintf("systembot: %s — %d user(s) (too many to list inline)", channel, len(users))
	}
	return fmt.Sprintf("systembot: %s (%d users): %s",
		channel, len(users), strings.Join(users, " "))
}

func (b *Bot) cmdRecent(args string) string {
	n := 10
	if s := strings.TrimSpace(args); s != "" {
		if parsed, err := strconv.Atoi(s); err == nil && parsed > 0 {
			n = parsed
		}
	}
	if n > 50 {
		n = 50
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.recent) == 0 {
		return "systembot: no events recorded yet"
	}
	start := 0
	if len(b.recent) > n {
		start = len(b.recent) - n
	}
	tail := b.recent[start:]

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("systembot: %d most recent event(s):\n", len(tail)))
	for _, e := range tail {
		sb.WriteString(fmt.Sprintf("  %s %s %s %s %s\n",
			e.At.Format("15:04:05"), e.Kind, e.Channel, e.Nick, e.Text))
	}
	return strings.TrimRight(sb.String(), "\n")
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
