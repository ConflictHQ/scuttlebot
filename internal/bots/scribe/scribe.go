// Package scribe implements the scribe bot — structured logging for all channel activity.
//
// scribe joins all configured channels, listens for PRIVMSG, and writes
// structured log entries to a Store. Valid JSON envelopes are logged with
// their parsed type and ID. Malformed messages are logged as raw entries
// without crashing. NOTICE messages are ignored (system/human commentary only).
package scribe

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lrstanley/girc"

	"github.com/conflicthq/scuttlebot/pkg/protocol"
)

const botNick = "scribe"

// Bot is the scribe logging bot.
type Bot struct {
	ircAddr  string
	password string
	channels []string
	store    Store
	log      *slog.Logger
	client   *girc.Client
}

// New creates a scribe Bot. channels is the list of channels to join and log.
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

// Start connects to IRC and begins logging. Blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) error {
	host, port, err := splitHostPort(b.ircAddr)
	if err != nil {
		return fmt.Errorf("scribe: parse irc addr: %w", err)
	}

	c := girc.New(girc.Config{
		Server: host,
		Port:   port,
		Nick:   botNick,
		User:   botNick,
		Name:   "scuttlebot scribe",
		SASL:   &girc.SASLPlain{User: botNick, Pass: b.password},
		SSL:    false,
	})

	c.Handlers.AddBg(girc.CONNECTED, func(client *girc.Client, e girc.Event) {
		for _, ch := range b.channels {
			client.Cmd.Join(ch)
		}
		b.log.Info("scribe connected and joined channels", "channels", b.channels)
	})

	// Log PRIVMSG — the agent message stream.
	c.Handlers.AddBg(girc.PRIVMSG, func(client *girc.Client, e girc.Event) {
		if len(e.Params) < 1 {
			return
		}
		channel := e.Params[0]
		if !strings.HasPrefix(channel, "#") {
			return // ignore DMs to scribe itself
		}
		text := e.Last()
		nick := e.Source.Name
		b.writeEntry(channel, nick, text)
	})

	// NOTICE is ignored — system/human commentary, not agent traffic.

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
		return fmt.Errorf("scribe: irc connection: %w", err)
	}
}

// Stop disconnects the bot.
func (b *Bot) Stop() {
	if b.client != nil {
		b.client.Close()
	}
}

func (b *Bot) writeEntry(channel, nick, text string) {
	entry := Entry{
		At:      time.Now(),
		Channel: channel,
		Nick:    nick,
		Raw:     text,
	}

	env, err := protocol.Unmarshal([]byte(text))
	if err != nil {
		// Not a valid envelope — log as raw. This is expected for human messages.
		entry.Kind = EntryKindRaw
	} else {
		entry.Kind = EntryKindEnvelope
		entry.MessageType = env.Type
		entry.MessageID = env.ID
	}

	if err := b.store.Append(entry); err != nil {
		b.log.Error("scribe: failed to write log entry", "err", err)
	}
}

func splitHostPort(addr string) (string, int, error) {
	var host string
	var port int
	if _, err := fmt.Sscanf(addr, "%[^:]:%d", &host, &port); err != nil {
		return "", 0, fmt.Errorf("invalid address %q: %w", addr, err)
	}
	return host, port, nil
}
