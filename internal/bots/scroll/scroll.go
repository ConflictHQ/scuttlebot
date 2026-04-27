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
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lrstanley/girc"

	"github.com/conflicthq/scuttlebot/internal/bots/scribe"
	"github.com/conflicthq/scuttlebot/pkg/chathistory"
	"github.com/conflicthq/scuttlebot/pkg/toon"
)

const (
	botNick         = "scroll"
	defaultLimit    = 50
	maxLimit        = 500
	rateLimitWindow = 10 * time.Second
)

// Bot is the scroll history-replay bot.
type Bot struct {
	nick      string
	ircAddr   string
	password  string
	channels  []string
	store     scribe.Store
	log       *slog.Logger
	client    *girc.Client
	history   *chathistory.Fetcher // nil until connected, if CHATHISTORY is available
	rateLimit sync.Map             // nick → last request time
}

// New creates a scroll Bot backed by the given scribe Store using the
// package default nick.
func New(ircAddr, password string, channels []string, store scribe.Store, log *slog.Logger) *Bot {
	return NewWithNick(botNick, ircAddr, password, channels, store, log)
}

// NewWithNick is the nick-aware constructor used by manager.buildBot so the
// operator's policy nick wins over the package default. See #173.
func NewWithNick(nick, ircAddr, password string, channels []string, store scribe.Store, log *slog.Logger) *Bot {
	if nick == "" {
		nick = botNick
	}
	return &Bot{
		nick:     nick,
		ircAddr:  ircAddr,
		password: password,
		channels: channels,
		store:    store,
		log:      log,
	}
}

// Name returns the bot's IRC nick.
func (b *Bot) Name() string { return b.nick }

// Start connects to IRC and begins handling replay requests. Blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) error {
	host, port, err := splitHostPort(b.ircAddr)
	if err != nil {
		return fmt.Errorf("scroll: parse irc addr: %w", err)
	}

	c := girc.New(girc.Config{
		Server:      host,
		Port:        port,
		Nick:        b.nick,
		User:        b.nick,
		Name:        "scuttlebot scroll",
		SASL:        &girc.SASLPlain{User: b.nick, Pass: b.password},
		PingDelay:   30 * time.Second,
		PingTimeout: 30 * time.Second,
		SSL:         false,
		SupportedCaps: map[string][]string{
			"draft/chathistory": nil,
			"chathistory":       nil,
		},
	})

	// Register CHATHISTORY batch handlers before connecting.
	b.history = chathistory.New(c)

	c.Handlers.AddBg(girc.CONNECTED, func(cl *girc.Client, e girc.Event) {
		cl.Cmd.Mode(cl.GetNick(), "+B")
		for _, ch := range b.channels {
			cl.Cmd.Join(ch)
		}
		hasCH := cl.HasCapability("chathistory") || cl.HasCapability("draft/chathistory")
		b.log.Info("scroll connected", "channels", b.channels, "chathistory", hasCH)
	})

	// Note: don't pre-register `replay` / `search` stubs via cmdparse —
	// that path returned "not implemented yet" before the real DM parser
	// at b.handle could reach the user. The real implementation lives in
	// handle() and parses the full grammar (last=N, since=<unix_ms>,
	// format=toon|json). See #165.

	c.Handlers.AddBg(girc.PRIVMSG, func(client *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil {
			return
		}
		target := e.Params[0]
		nick := e.Source.Name
		text := strings.TrimSpace(e.Last())

		if strings.HasPrefix(target, "#") {
			if reply := matchAddressed(b.nick, nick, text); reply != "" {
				client.Cmd.Message(target, reply)
			}
			return
		}

		// DM path: trap greetings before the strict replay parser.
		if isGreetOrHelp(text) {
			client.Cmd.Notice(nick, fmt.Sprintf("scroll here — I replay channel history. Try: replay #channel [last=N] [since=<unix_ms>] [format=json|toon]"))
			return
		}
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
		client.Cmd.Notice(nick, "usage: replay #channel [last=N] [since=<unix_ms>] [format=json|toon]")
		return
	}

	entries, err := b.fetchHistory(req)
	if err != nil {
		client.Cmd.Notice(nick, fmt.Sprintf("error fetching history: %s", err))
		return
	}

	if len(entries) == 0 {
		client.Cmd.Notice(nick, fmt.Sprintf("no history found for %s", req.Channel))
		return
	}

	if req.Format == "toon" {
		toonEntries := make([]toon.Entry, len(entries))
		for i, e := range entries {
			toonEntries[i] = toon.Entry{
				Nick:        e.Nick,
				MessageType: e.MessageType,
				Text:        e.Raw,
				At:          e.At,
			}
		}
		output := toon.Format(toonEntries, toon.Options{Channel: req.Channel})
		for _, line := range strings.Split(output, "\n") {
			if line != "" {
				client.Cmd.Notice(nick, line)
			}
		}
	} else {
		client.Cmd.Notice(nick, fmt.Sprintf("--- replay %s (%d entries) ---", req.Channel, len(entries)))
		for _, e := range entries {
			line, _ := json.Marshal(e)
			client.Cmd.Notice(nick, string(line))
		}
		client.Cmd.Notice(nick, fmt.Sprintf("--- end replay %s ---", req.Channel))
	}
}

// fetchHistory tries CHATHISTORY first, falls back to scribe store.
func (b *Bot) fetchHistory(req *replayRequest) ([]scribe.Entry, error) {
	if b.history != nil && b.client != nil {
		hasCH := b.client.HasCapability("chathistory") || b.client.HasCapability("draft/chathistory")
		if hasCH {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			msgs, err := b.history.Latest(ctx, req.Channel, req.Limit)
			if err == nil {
				entries := make([]scribe.Entry, len(msgs))
				for i, m := range msgs {
					entries[i] = scribe.Entry{
						At:      m.At,
						Channel: req.Channel,
						Nick:    m.Nick,
						Kind:    scribe.EntryKindRaw,
						Raw:     m.Text,
					}
					if m.Account != "" {
						entries[i].Nick = m.Account
					}
				}
				return entries, nil
			}
			b.log.Warn("chathistory failed, falling back to store", "err", err)
		}
	}
	return b.store.Query(req.Channel, req.Limit)
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
	Since   int64  // unix ms, 0 = no filter
	Format  string // "json" (default) or "toon"
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
		case "format":
			switch strings.ToLower(kv[1]) {
			case "json", "toon":
				req.Format = strings.ToLower(kv[1])
			default:
				return nil, fmt.Errorf("unknown format %q (use json or toon)", kv[1])
			}
		default:
			return nil, fmt.Errorf("unknown argument %q", kv[0])
		}
	}

	return req, nil
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

// matchAddressed returns a friendly response when text addresses our nick in
// a channel ("scroll: hi"). Real replay/search work runs from DMs.
func matchAddressed(myNick, fromNick, text string) string {
	lower := strings.ToLower(text)
	prefixes := []string{strings.ToLower(myNick) + ": ", strings.ToLower(myNick) + ":", strings.ToLower(myNick) + ", "}
	rest := ""
	matched := false
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			rest = strings.TrimSpace(text[len(p):])
			matched = true
			break
		}
	}
	if !matched || rest == "" {
		return ""
	}
	if isGreetOrHelp(rest) {
		return fmt.Sprintf("hi %s — I'm %s, the channel-history replay bot. Send me a DM with: replay #channel [last=N] [since=<unix_ms>] [format=json|toon]", fromNick, myNick)
	}
	return fmt.Sprintf("%s: %s — I run from DMs only. Send me a private message with: replay #channel [last=N]", fromNick, myNick)
}

func isGreetOrHelp(text string) bool {
	w := strings.ToLower(strings.TrimRight(strings.Fields(text + " ")[0], "!?.,"))
	switch w {
	case "hi", "hello", "hey", "yo", "sup", "hola", "howdy", "greetings", "ping", "help", "?":
		return true
	}
	return false
}
