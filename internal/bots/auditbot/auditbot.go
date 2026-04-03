// Package auditbot implements the auditbot — immutable agent action audit trail.
//
// auditbot answers: "what did agent X do, and when?"
//
// It records two categories of events:
//  1. IRC-observed: agent envelopes whose type appears in the configured
//     auditTypes set (e.g. task.create, agent.hello).
//  2. Registry-injected: credential lifecycle events (registration, rotation,
//     revocation) written directly via Record(), not via IRC.
//
// Entries are append-only. There are no update or delete operations.
package auditbot

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/lrstanley/girc"

	"github.com/conflicthq/scuttlebot/pkg/protocol"
)

const botNick = "auditbot"

// EventKind classifies the source of an audit entry.
type EventKind string

const (
	// KindIRC indicates the event was observed on the IRC message stream.
	KindIRC EventKind = "irc"
	// KindRegistry indicates the event was injected by the registry.
	KindRegistry EventKind = "registry"
)

// Entry is an immutable audit record.
type Entry struct {
	At          time.Time
	Kind        EventKind
	Channel     string // empty for registry events
	Nick        string // agent nick
	MessageType string // e.g. "task.create", "agent.registered"
	MessageID   string // envelope ID for IRC events; empty for registry events
	Detail      string // human-readable detail (reason, etc.)
}

// Store persists audit entries. Implementations must be append-only.
type Store interface {
	Append(Entry) error
}

// Bot is the auditbot.
type Bot struct {
	ircAddr    string
	password   string
	channels   []string
	auditTypes map[string]struct{}
	store      Store
	log        *slog.Logger
	client     *girc.Client
}

// New creates an auditbot. auditTypes is the set of message types to record;
// pass nil or empty to audit all envelope types.
func New(ircAddr, password string, channels []string, auditTypes []string, store Store, log *slog.Logger) *Bot {
	at := make(map[string]struct{}, len(auditTypes))
	for _, t := range auditTypes {
		at[t] = struct{}{}
	}
	return &Bot{
		ircAddr:    ircAddr,
		password:   password,
		channels:   channels,
		auditTypes: at,
		store:      store,
		log:        log,
	}
}

// Name returns the bot's IRC nick.
func (b *Bot) Name() string { return botNick }

// Record writes a registry lifecycle event directly to the audit store.
// This is called by the registry on registration, rotation, and revocation —
// not from IRC.
func (b *Bot) Record(nick, eventType, detail string) {
	b.write(Entry{
		Kind:        KindRegistry,
		Nick:        nick,
		MessageType: eventType,
		Detail:      detail,
	})
}

// Start connects to IRC and begins auditing. Blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) error {
	host, port, err := splitHostPort(b.ircAddr)
	if err != nil {
		return fmt.Errorf("auditbot: parse irc addr: %w", err)
	}

	c := girc.New(girc.Config{
		Server: host,
		Port:   port,
		Nick:   botNick,
		User:   botNick,
		Name:   "scuttlebot auditbot",
		SASL:   &girc.SASLPlain{User: botNick, Pass: b.password},
		SSL:    false,
	})

	c.Handlers.AddBg(girc.CONNECTED, func(cl *girc.Client, _ girc.Event) {
		for _, ch := range b.channels {
			cl.Cmd.Join(ch)
		}
		b.log.Info("auditbot connected", "channels", b.channels, "audit_types", b.auditTypesList())
	})

	c.Handlers.AddBg(girc.INVITE, func(cl *girc.Client, e girc.Event) {
		if ch := e.Last(); strings.HasPrefix(ch, "#") {
			cl.Cmd.Join(ch)
		}
	})

	c.Handlers.AddBg(girc.PRIVMSG, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 {
			return
		}
		channel := e.Params[0]
		if !strings.HasPrefix(channel, "#") {
			return // ignore DMs
		}
		text := e.Last()
		env, err := protocol.Unmarshal([]byte(text))
		if err != nil {
			return // non-envelope PRIVMSG ignored
		}
		if !b.shouldAudit(env.Type) {
			return
		}
		nick := ""
		if e.Source != nil {
			nick = e.Source.Name
		}
		b.write(Entry{
			Kind:        KindIRC,
			Channel:     channel,
			Nick:        nick,
			MessageType: env.Type,
			MessageID:   env.ID,
		})
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
		return fmt.Errorf("auditbot: irc connection: %w", err)
	}
}

// Stop disconnects the bot.
func (b *Bot) Stop() {
	if b.client != nil {
		b.client.Close()
	}
}

func (b *Bot) shouldAudit(msgType string) bool {
	if len(b.auditTypes) == 0 {
		return true // audit everything when no filter configured
	}
	_, ok := b.auditTypes[msgType]
	return ok
}

func (b *Bot) write(e Entry) {
	e.At = time.Now()
	if err := b.store.Append(e); err != nil {
		b.log.Error("auditbot: failed to write entry", "type", e.MessageType, "err", err)
	}
}

func (b *Bot) auditTypesList() []string {
	if len(b.auditTypes) == 0 {
		return []string{"*"}
	}
	out := make([]string, 0, len(b.auditTypes))
	for t := range b.auditTypes {
		out = append(out, t)
	}
	return out
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
