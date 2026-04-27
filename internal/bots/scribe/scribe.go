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
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lrstanley/girc"

	"github.com/conflicthq/scuttlebot/internal/bots/cmdparse"
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
		Server:      host,
		Port:        port,
		Nick:        botNick,
		User:        botNick,
		Name:        "scuttlebot scribe",
		SASL:        &girc.SASLPlain{User: botNick, Pass: b.password},
		PingDelay:   30 * time.Second,
		PingTimeout: 30 * time.Second,
		SSL:         false,
	})

	c.Handlers.AddBg(girc.CONNECTED, func(client *girc.Client, e girc.Event) {
		client.Cmd.Mode(client.GetNick(), "+B")
		for _, ch := range b.channels {
			client.Cmd.Join(ch)
		}
		b.log.Info("scribe connected and joined channels", "channels", b.channels)
	})

	c.Handlers.AddBg(girc.INVITE, func(cl *girc.Client, e girc.Event) {
		if ch := e.Last(); strings.HasPrefix(ch, "#") {
			cl.Cmd.Join(ch)
		}
	})

	router := cmdparse.NewRouter(botNick)
	router.SetPurpose("the channel logger — writes a structured log of every message in joined channels")
	router.Register(cmdparse.Command{
		Name:        "tail",
		Usage:       "TAIL [#channel] [N]",
		Description: "show the last N entries (default 5)",
		Handler: func(ctx *cmdparse.Context, args string) string {
			return b.cmdTail(ctx, args)
		},
	})
	router.Register(cmdparse.Command{
		Name:        "search",
		Usage:       "SEARCH <term> [#channel]",
		Description: "case-insensitive substring search of recent log entries",
		Handler: func(ctx *cmdparse.Context, args string) string {
			return b.cmdSearch(ctx, args)
		},
	})
	router.Register(cmdparse.Command{
		Name:        "stats",
		Usage:       "STATS [#channel]",
		Description: "show entry counts by channel and message type",
		Handler: func(ctx *cmdparse.Context, args string) string {
			return b.cmdStats(ctx, args)
		},
	})

	// Log PRIVMSG — the agent message stream.
	c.Handlers.AddBg(girc.PRIVMSG, func(client *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil {
			return
		}
		// Dispatch commands (DMs and channel messages).
		if reply := router.Dispatch(e.Source.Name, e.Params[0], e.Last()); reply != nil {
			for _, line := range reply.Lines() {
				client.Cmd.Message(reply.Target, line)
			}
			return
		}
		channel := e.Params[0]
		if !strings.HasPrefix(channel, "#") {
			return // non-command DMs ignored
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

// cmdTail returns the most recent log entries. In a channel context the
// channel defaults to ctx.Channel; in a DM the operator must name the channel.
func (b *Bot) cmdTail(ctx *cmdparse.Context, args string) string {
	channel, n, err := parseChannelAndN(ctx, args, 5)
	if err != nil {
		return err.Error()
	}
	entries, err := b.store.Query(channel, n)
	if err != nil {
		return fmt.Sprintf("scribe: query error: %v", err)
	}
	if len(entries) == 0 {
		if channel == "" {
			return "scribe: no entries logged yet"
		}
		return fmt.Sprintf("scribe: no entries for %s", channel)
	}
	return formatEntries(entries)
}

func (b *Bot) cmdSearch(ctx *cmdparse.Context, args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return "scribe: usage — SEARCH <term> [#channel]"
	}

	// Split term/optional channel. Tolerates "search foo #general" or "search foo".
	fields := strings.Fields(args)
	channel := ctx.Channel
	for i := len(fields) - 1; i >= 0; i-- {
		if strings.HasPrefix(fields[i], "#") {
			channel = fields[i]
			fields = append(fields[:i], fields[i+1:]...)
			break
		}
	}
	term := strings.Join(fields, " ")
	if term == "" {
		return "scribe: usage — SEARCH <term> [#channel]"
	}

	// Search the most recent N entries to keep the work bounded.
	const lookback = 1000
	entries, err := b.store.Query(channel, lookback)
	if err != nil {
		return fmt.Sprintf("scribe: query error: %v", err)
	}
	needle := strings.ToLower(term)
	var hits []Entry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Raw), needle) {
			hits = append(hits, e)
		}
	}
	if len(hits) == 0 {
		return fmt.Sprintf("scribe: no matches for %q in last %d entries", term, lookback)
	}
	if len(hits) > 10 {
		hits = hits[len(hits)-10:]
	}
	return fmt.Sprintf("scribe: %d match(es) for %q (showing last 10):\n%s", len(hits), term, formatEntries(hits))
}

func (b *Bot) cmdStats(ctx *cmdparse.Context, args string) string {
	channel := strings.TrimSpace(args)
	if channel == "" {
		channel = ctx.Channel
	}
	entries, err := b.store.Query(channel, 0)
	if err != nil {
		return fmt.Sprintf("scribe: query error: %v", err)
	}
	if len(entries) == 0 {
		if channel == "" {
			return "scribe: no entries logged yet"
		}
		return fmt.Sprintf("scribe: no entries for %s", channel)
	}

	byChannel := make(map[string]int)
	byType := make(map[string]int)
	envelopes, raws := 0, 0
	uniqueNicks := make(map[string]struct{})
	for _, e := range entries {
		byChannel[e.Channel]++
		uniqueNicks[e.Nick] = struct{}{}
		switch e.Kind {
		case EntryKindEnvelope:
			envelopes++
			if e.MessageType != "" {
				byType[e.MessageType]++
			}
		case EntryKindRaw:
			raws++
		}
	}

	var sb strings.Builder
	scope := "all channels"
	if channel != "" {
		scope = channel
	}
	sb.WriteString(fmt.Sprintf("scribe: %d entries across %s | %d envelope, %d raw | %d unique nick(s)\n",
		len(entries), scope, envelopes, raws, len(uniqueNicks)))
	if channel == "" && len(byChannel) > 0 {
		sb.WriteString("by channel:")
		channels := sortedKeys(byChannel)
		for _, ch := range channels {
			sb.WriteString(fmt.Sprintf(" %s(%d)", ch, byChannel[ch]))
		}
		sb.WriteString("\n")
	}
	if len(byType) > 0 {
		sb.WriteString("envelope types:")
		types := sortedKeys(byType)
		for _, t := range types {
			sb.WriteString(fmt.Sprintf(" %s(%d)", t, byType[t]))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// parseChannelAndN extracts a channel argument and an optional integer N from
// the cmdparse args string. Defaults the channel to ctx.Channel and N to def.
func parseChannelAndN(ctx *cmdparse.Context, args string, def int) (channel string, n int, err error) {
	n = def
	channel = ctx.Channel
	for _, f := range strings.Fields(args) {
		if strings.HasPrefix(f, "#") {
			channel = f
			continue
		}
		parsed, perr := strconv.Atoi(f)
		if perr != nil || parsed <= 0 {
			return "", 0, fmt.Errorf("scribe: bad argument %q — expected a positive integer or #channel", f)
		}
		if parsed > 50 {
			parsed = 50 // cap to keep IRC replies sane
		}
		n = parsed
	}
	if channel == "" {
		return "", 0, fmt.Errorf("scribe: in a DM you must name a #channel")
	}
	return channel, n, nil
}

func formatEntries(entries []Entry) string {
	var sb strings.Builder
	for _, e := range entries {
		text := e.Raw
		if e.Kind == EntryKindEnvelope && e.MessageType != "" {
			text = fmt.Sprintf("[%s] %s", e.MessageType, e.Raw)
		}
		if len(text) > 180 {
			text = text[:177] + "..."
		}
		sb.WriteString(fmt.Sprintf("  %s %s <%s> %s\n",
			e.At.Format("15:04:05"), e.Channel, e.Nick, text))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
