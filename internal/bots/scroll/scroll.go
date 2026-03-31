// Package scroll implements the scroll bot — channel history replay via PM.
//
// Agents or humans send a PM to scroll requesting history for a channel.
// scroll fetches from scribe's Store and delivers entries as PM messages,
// never posting to the channel itself.
//
// Command format:
//
//	replay #channel [last=N] [since=<unix_ms>]
package scroll

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lrstanley/girc"

	"github.com/conflicthq/scuttlebot/internal/bots/scribe"
)

const (
	botNick         = "scroll"
	defaultLimit    = 50
	maxLimit        = 500
	rateLimitWindow = 10 * time.Second
)

// Bot is the scroll history-replay bot.
type Bot struct {
	ircAddr   string
	password  string
	store     scribe.Store
	log       *slog.Logger
	client    *girc.Client
	rateLimit sync.Map // nick → last request time
}

// New creates a scroll Bot backed by the given scribe Store.
func New(ircAddr, password string, store scribe.Store, log *slog.Logger) *Bot {
	return &Bot{
		ircAddr:  ircAddr,
		password: password,
		store:    store,
		log:      log,
	}
}

// Name returns the bot's IRC nick.
func (b *Bot) Name() string { return botNick }

// Start connects to IRC and begins handling replay requests. Blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) error {
	host, port, err := splitHostPort(b.ircAddr)
	if err != nil {
		return fmt.Errorf("scroll: parse irc addr: %w", err)
	}

	c := girc.New(girc.Config{
		Server: host,
		Port:   port,
		Nick:   botNick,
		User:   botNick,
		Name:   "scuttlebot scroll",
		SASL:   &girc.SASLPlain{User: botNick, Pass: b.password},
		SSL:    false,
	})

	c.Handlers.AddBg(girc.CONNECTED, func(client *girc.Client, e girc.Event) {
		b.log.Info("scroll connected")
	})

	// Only respond to DMs — ignore anything in a channel.
	c.Handlers.AddBg(girc.PRIVMSG, func(client *girc.Client, e girc.Event) {
		if len(e.Params) < 1 {
			return
		}
		target := e.Params[0]
		if strings.HasPrefix(target, "#") {
			return // channel message, ignore
		}
		nick := e.Source.Name
		text := strings.TrimSpace(e.Last())
		b.handle(client, nick, text)
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
		return fmt.Errorf("scroll: irc connection: %w", err)
	}
}

// Stop disconnects the bot.
func (b *Bot) Stop() {
	if b.client != nil {
		b.client.Close()
	}
}

func (b *Bot) handle(client *girc.Client, nick, text string) {
	if !b.checkRateLimit(nick) {
		client.Cmd.Notice(nick, "rate limited — please wait before requesting again")
		return
	}

	req, err := ParseCommand(text)
	if err != nil {
		client.Cmd.Notice(nick, fmt.Sprintf("error: %s", err))
		client.Cmd.Notice(nick, "usage: replay #channel [last=N] [since=<unix_ms>]")
		return
	}

	entries, err := b.store.Query(req.Channel, req.Limit)
	if err != nil {
		client.Cmd.Notice(nick, fmt.Sprintf("error fetching history: %s", err))
		return
	}

	if len(entries) == 0 {
		client.Cmd.Notice(nick, fmt.Sprintf("no history found for %s", req.Channel))
		return
	}

	client.Cmd.Notice(nick, fmt.Sprintf("--- replay %s (%d entries) ---", req.Channel, len(entries)))
	for _, e := range entries {
		line, _ := json.Marshal(e)
		client.Cmd.Notice(nick, string(line))
	}
	client.Cmd.Notice(nick, fmt.Sprintf("--- end replay %s ---", req.Channel))
}

func (b *Bot) checkRateLimit(nick string) bool {
	now := time.Now()
	if last, ok := b.rateLimit.Load(nick); ok {
		if now.Sub(last.(time.Time)) < rateLimitWindow {
			return false
		}
	}
	b.rateLimit.Store(nick, now)
	return true
}

// ReplayRequest is a parsed replay command.
type replayRequest struct {
	Channel string
	Limit   int
	Since   int64 // unix ms, 0 = no filter
}

// ParseCommand parses a replay command string. Exported for testing.
func ParseCommand(text string) (*replayRequest, error) {
	parts := strings.Fields(text)
	if len(parts) < 2 || strings.ToLower(parts[0]) != "replay" {
		return nil, fmt.Errorf("unknown command %q", parts[0])
	}

	channel := parts[1]
	if !strings.HasPrefix(channel, "#") {
		return nil, fmt.Errorf("channel must start with #")
	}

	req := &replayRequest{Channel: channel, Limit: defaultLimit}

	for _, arg := range parts[2:] {
		kv := strings.SplitN(arg, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("invalid argument %q (expected key=value)", arg)
		}
		switch strings.ToLower(kv[0]) {
		case "last":
			n, err := strconv.Atoi(kv[1])
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("invalid last=%q (must be a positive integer)", kv[1])
			}
			if n > maxLimit {
				n = maxLimit
			}
			req.Limit = n
		case "since":
			ts, err := strconv.ParseInt(kv[1], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid since=%q (must be unix milliseconds)", kv[1])
			}
			req.Since = ts
		default:
			return nil, fmt.Errorf("unknown argument %q", kv[0])
		}
	}

	return req, nil
}

func splitHostPort(addr string) (string, int, error) {
	var host string
	var port int
	if _, err := fmt.Sscanf(addr, "%[^:]:%d", &host, &port); err != nil {
		return "", 0, fmt.Errorf("invalid address %q: %w", addr, err)
	}
	return host, port, nil
}
