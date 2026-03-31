// Package oracle implements the oracle bot — on-demand channel summarization.
//
// Agents and humans send oracle a DM:
//
//	PRIVMSG oracle :summarize #fleet [last=50] [format=toon|json]
//
// oracle fetches recent messages from the channel history store, calls the
// configured LLM provider for summarization, and replies in PM via NOTICE.
// Output format is either TOON (default, token-efficient) or JSON.
//
// oracle never sends to channels — only PM replies.
package oracle

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
)

const (
	botNick       = "oracle"
	defaultLimit  = 50
	maxLimit      = 200
	rateLimitWait = 30 * time.Second
)

// Format is the output format for oracle responses.
type Format string

const (
	FormatTOON Format = "toon"
	FormatJSON Format = "json"
)

// HistoryEntry is a single message from channel history.
type HistoryEntry struct {
	Nick        string
	MessageType string // empty for raw/human messages
	Raw         string
}

// HistoryFetcher retrieves recent messages from a channel.
type HistoryFetcher interface {
	Query(channel string, limit int) ([]HistoryEntry, error)
}

// LLMProvider calls a language model for summarization.
// Implementations are pluggable — oracle does not hardcode any provider.
type LLMProvider interface {
	Summarize(ctx context.Context, prompt string) (string, error)
}

// SummarizeRequest is a parsed oracle command.
type SummarizeRequest struct {
	Channel string
	Limit   int
	Format  Format
}

// ParseCommand parses "summarize #channel [last=N] [format=toon|json]".
// Returns an error for malformed input; ignores unrecognised tokens.
func ParseCommand(text string) (*SummarizeRequest, error) {
	text = strings.TrimSpace(text)
	parts := strings.Fields(text)
	if len(parts) < 2 {
		return nil, fmt.Errorf("usage: summarize <#channel> [last=N] [format=toon|json]")
	}
	if !strings.EqualFold(parts[0], "summarize") {
		return nil, fmt.Errorf("unknown command %q", parts[0])
	}

	req := &SummarizeRequest{
		Channel: parts[1],
		Limit:   defaultLimit,
		Format:  FormatTOON,
	}

	if !strings.HasPrefix(req.Channel, "#") {
		return nil, fmt.Errorf("channel must start with # (got %q)", req.Channel)
	}

	for _, token := range parts[2:] {
		kv := strings.SplitN(token, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch strings.ToLower(kv[0]) {
		case "last":
			n, err := strconv.Atoi(kv[1])
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("last= must be a positive integer")
			}
			if n > maxLimit {
				n = maxLimit
			}
			req.Limit = n
		case "format":
			switch Format(strings.ToLower(kv[1])) {
			case FormatTOON:
				req.Format = FormatTOON
			case FormatJSON:
				req.Format = FormatJSON
			default:
				return nil, fmt.Errorf("format must be toon or json (got %q)", kv[1])
			}
		}
	}
	return req, nil
}

// Bot is the oracle bot.
type Bot struct {
	ircAddr  string
	password string
	history  HistoryFetcher
	llm      LLMProvider
	log      *slog.Logger
	mu       sync.Mutex
	lastReq  map[string]time.Time // nick → last request time
	client   *girc.Client
}

// New creates an oracle bot.
func New(ircAddr, password string, history HistoryFetcher, llm LLMProvider, log *slog.Logger) *Bot {
	return &Bot{
		ircAddr:  ircAddr,
		password: password,
		history:  history,
		llm:      llm,
		log:      log,
		lastReq:  make(map[string]time.Time),
	}
}

// Name returns the bot's IRC nick.
func (b *Bot) Name() string { return botNick }

// Start connects to IRC and begins serving summarization requests.
func (b *Bot) Start(ctx context.Context) error {
	host, port, err := splitHostPort(b.ircAddr)
	if err != nil {
		return fmt.Errorf("oracle: parse irc addr: %w", err)
	}

	c := girc.New(girc.Config{
		Server: host,
		Port:   port,
		Nick:   botNick,
		User:   botNick,
		Name:   "scuttlebot oracle",
		SASL:   &girc.SASLPlain{User: botNick, Pass: b.password},
		SSL:    false,
	})

	c.Handlers.AddBg(girc.CONNECTED, func(_ *girc.Client, _ girc.Event) {
		if b.log != nil {
			b.log.Info("oracle connected")
		}
	})

	// Only handle DMs — oracle ignores channel messages.
	c.Handlers.AddBg(girc.PRIVMSG, func(cl *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil {
			return
		}
		target := e.Params[0]
		if strings.HasPrefix(target, "#") {
			return // channel message — ignore
		}
		nick := e.Source.Name
		text := e.Last()

		go b.handle(ctx, cl, nick, text)
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
		return fmt.Errorf("oracle: irc connection: %w", err)
	}
}

// Stop disconnects the bot.
func (b *Bot) Stop() {
	if b.client != nil {
		b.client.Close()
	}
}

func (b *Bot) handle(ctx context.Context, cl *girc.Client, nick, text string) {
	req, err := ParseCommand(text)
	if err != nil {
		cl.Cmd.Notice(nick, "oracle: "+err.Error())
		return
	}

	// Rate limit.
	b.mu.Lock()
	if last, ok := b.lastReq[nick]; ok && time.Since(last) < rateLimitWait {
		wait := rateLimitWait - time.Since(last)
		b.mu.Unlock()
		cl.Cmd.Notice(nick, fmt.Sprintf("oracle: rate limited — try again in %s", wait.Round(time.Second)))
		return
	}
	b.lastReq[nick] = time.Now()
	b.mu.Unlock()

	// Fetch history.
	entries, err := b.history.Query(req.Channel, req.Limit)
	if err != nil {
		cl.Cmd.Notice(nick, fmt.Sprintf("oracle: failed to fetch history for %s: %v", req.Channel, err))
		return
	}
	if len(entries) == 0 {
		cl.Cmd.Notice(nick, fmt.Sprintf("oracle: no history found for %s", req.Channel))
		return
	}

	// Build prompt.
	prompt := buildPrompt(req.Channel, entries)

	// Call LLM.
	summary, err := b.llm.Summarize(ctx, prompt)
	if err != nil {
		cl.Cmd.Notice(nick, "oracle: summarization failed: "+err.Error())
		return
	}

	// Format and deliver.
	response := formatResponse(req.Channel, len(entries), summary, req.Format)
	for _, line := range strings.Split(response, "\n") {
		if line != "" {
			cl.Cmd.Notice(nick, line)
		}
	}
}

func buildPrompt(channel string, entries []HistoryEntry) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Summarize the following IRC conversation from %s.\n", channel)
	fmt.Fprintf(&sb, "Focus on: key decisions, actions taken, outstanding tasks, and important context.\n")
	fmt.Fprintf(&sb, "Be concise. %d messages:\n\n", len(entries))
	for _, e := range entries {
		if e.MessageType != "" {
			fmt.Fprintf(&sb, "[%s] (type=%s) %s\n", e.Nick, e.MessageType, e.Raw)
		} else {
			fmt.Fprintf(&sb, "[%s] %s\n", e.Nick, e.Raw)
		}
	}
	return sb.String()
}

func formatResponse(channel string, count int, summary string, format Format) string {
	switch format {
	case FormatJSON:
		// Simple JSON — avoid encoding/json dependency in the hot path.
		return fmt.Sprintf(`{"channel":%q,"messages":%d,"format":"json","summary":%q}`,
			channel, count, summary)
	default: // TOON
		return fmt.Sprintf("--- oracle summary: %s (%d messages) ---\n%s\n--- end ---",
			channel, count, summary)
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
