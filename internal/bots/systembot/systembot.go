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
	"strconv"
	"strings"
	"time"

	"github.com/lrstanley/girc"
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
}

// New creates a systembot.
func New(ircAddr, password string, channels []string, store Store, log *slog.Logger) *Bot {
	return &Bot{
		ircAddr:  ircAddr,
		password: password,
		channels: channels,
		store:    store,
		log:      log,
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
