// Package bridge implements the IRC bridge bot for the web chat UI.
//
// The bridge connects to IRC, joins channels, and buffers recent messages.
// It exposes subscriptions for SSE fan-out and a Send method for the web UI
// to post messages back into IRC.
package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lrstanley/girc"
)

const botNick = "bridge"
const defaultWebUserTTL = 5 * time.Minute

// Message is a single IRC message captured by the bridge.
type Message struct {
	At      time.Time `json:"at"`
	Channel string    `json:"channel"`
	Nick    string    `json:"nick"`
	Text    string    `json:"text"`
}

// ringBuf is a fixed-capacity circular buffer of Messages.
type ringBuf struct {
	msgs []Message
	head int
	size int
	cap  int
}

func newRingBuf(cap int) *ringBuf {
	return &ringBuf{msgs: make([]Message, cap), cap: cap}
}

func (r *ringBuf) push(m Message) {
	r.msgs[r.head] = m
	r.head = (r.head + 1) % r.cap
	if r.size < r.cap {
		r.size++
	}
}

// snapshot returns messages in chronological order (oldest first).
func (r *ringBuf) snapshot() []Message {
	if r.size == 0 {
		return nil
	}
	out := make([]Message, r.size)
	if r.size < r.cap {
		copy(out, r.msgs[:r.size])
	} else {
		n := copy(out, r.msgs[r.head:])
		copy(out[n:], r.msgs[:r.head])
	}
	return out
}

// Stats is a snapshot of bridge activity.
type Stats struct {
	Channels      int   `json:"channels"`
	MessagesTotal int64 `json:"messages_total"`
	ActiveSubs    int   `json:"active_subscribers"`
}

// Bot is the IRC bridge bot.
type Bot struct {
	ircAddr      string
	nick         string
	password     string
	bufSize      int
	initChannels []string
	log          *slog.Logger

	mu      sync.RWMutex
	buffers map[string]*ringBuf
	subs    map[string]map[uint64]chan Message
	subSeq  uint64
	joined  map[string]bool
	// webUsers tracks nicks that have posted via the HTTP bridge recently.
	// channel → nick → last seen time
	webUsers map[string]map[string]time.Time
	// webUserTTL controls how long bridge-posted HTTP nicks stay visible in Users().
	webUserTTL time.Duration

	msgTotal atomic.Int64

	joinCh chan string
	client *girc.Client
}

// New creates a bridge Bot.
func New(ircAddr, nick, password string, channels []string, bufSize int, webUserTTL time.Duration, log *slog.Logger) *Bot {
	if nick == "" {
		nick = botNick
	}
	if bufSize <= 0 {
		bufSize = 200
	}
	if webUserTTL <= 0 {
		webUserTTL = defaultWebUserTTL
	}
	// Normalize channel names: ensure # prefix.
	for i, ch := range channels {
		if ch != "" && ch[0] != '#' {
			channels[i] = "#" + ch
		}
	}
	return &Bot{
		ircAddr:      ircAddr,
		nick:         nick,
		password:     password,
		bufSize:      bufSize,
		initChannels: channels,
		webUsers:     make(map[string]map[string]time.Time),
		webUserTTL:   webUserTTL,
		log:          log,
		buffers:      make(map[string]*ringBuf),
		subs:         make(map[string]map[uint64]chan Message),
		joined:       make(map[string]bool),
		joinCh:       make(chan string, 32),
	}
}

// SetWebUserTTL updates how long bridge-posted HTTP nicks remain visible in
// the channel user list after their last post.
func (b *Bot) SetWebUserTTL(ttl time.Duration) {
	if ttl <= 0 {
		ttl = defaultWebUserTTL
	}
	b.mu.Lock()
	b.webUserTTL = ttl
	b.mu.Unlock()
}

// Name returns the bot's IRC nick.
func (b *Bot) Name() string { return b.nick }

// Start connects to IRC and begins bridging messages. Blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) error {
	host, port, err := splitHostPort(b.ircAddr)
	if err != nil {
		return fmt.Errorf("bridge: parse irc addr: %w", err)
	}

	c := girc.New(girc.Config{
		Server:      host,
		Port:        port,
		Nick:        b.nick,
		User:        b.nick,
		Name:        "scuttlebot bridge",
		SASL:        &girc.SASLPlain{User: b.nick, Pass: b.password},
		PingDelay:   30 * time.Second,
		PingTimeout: 30 * time.Second,
		SSL:         false,
	})

	c.Handlers.AddBg(girc.CONNECTED, func(cl *girc.Client, _ girc.Event) {
		if b.log != nil {
			b.log.Info("bridge connected")
		}
		for _, ch := range b.initChannels {
			cl.Cmd.Join(ch)
		}
	})

	c.Handlers.AddBg(girc.INVITE, func(_ *girc.Client, e girc.Event) {
		if ch := e.Last(); strings.HasPrefix(ch, "#") {
			b.JoinChannel(ch)
		}
	})

	c.Handlers.AddBg(girc.JOIN, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil || e.Source.Name != b.nick {
			return
		}
		channel := e.Params[0]
		b.mu.Lock()
		if !b.joined[channel] {
			b.joined[channel] = true
			if b.buffers[channel] == nil {
				b.buffers[channel] = newRingBuf(b.bufSize)
				b.subs[channel] = make(map[uint64]chan Message)
			}
		}
		b.mu.Unlock()
		if b.log != nil {
			b.log.Info("bridge joined channel", "channel", channel)
		}
	})

	c.Handlers.AddBg(girc.PRIVMSG, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil {
			return
		}
		channel := e.Params[0]
		if !strings.HasPrefix(channel, "#") {
			return // ignore DMs
		}
		b.dispatch(Message{
			At:      time.Now(),
			Channel: channel,
			Nick:    e.Source.Name,
			Text:    e.Last(),
		})
	})

	b.client = c

	errCh := make(chan error, 1)
	go func() {
		if err := c.Connect(); err != nil && ctx.Err() == nil {
			errCh <- err
		}
	}()

	go b.joinLoop(ctx, c)

	select {
	case <-ctx.Done():
		c.Close()
		return nil
	case err := <-errCh:
		return fmt.Errorf("bridge: irc: %w", err)
	}
}

// Stop disconnects the bot.
func (b *Bot) Stop() {
	if b.client != nil {
		b.client.Close()
	}
}

// JoinChannel asks the bridge to join a channel it isn't already in.
// Pre-initialises the buffer so Messages() returns an empty slice (not nil)
// immediately, even before the IRC JOIN is confirmed.
func (b *Bot) JoinChannel(channel string) {
	b.mu.Lock()
	if b.buffers[channel] == nil {
		b.buffers[channel] = newRingBuf(b.bufSize)
		b.subs[channel] = make(map[uint64]chan Message)
	}
	b.mu.Unlock()
	select {
	case b.joinCh <- channel:
	default:
	}
}

// LeaveChannel parts the bridge from a channel and removes its buffers.
func (b *Bot) LeaveChannel(channel string) {
	if b.client != nil {
		b.client.Cmd.Part(channel)
	}
	b.mu.Lock()
	delete(b.joined, channel)
	delete(b.buffers, channel)
	delete(b.subs, channel)
	b.mu.Unlock()
}

// Channels returns the list of channels currently joined.
func (b *Bot) Channels() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, 0, len(b.joined))
	for ch := range b.joined {
		out = append(out, ch)
	}
	return out
}

// Messages returns a snapshot of buffered messages for channel, oldest first.
// Returns nil if the channel is unknown.
func (b *Bot) Messages(channel string) []Message {
	b.mu.RLock()
	defer b.mu.RUnlock()
	rb := b.buffers[channel]
	if rb == nil {
		return nil
	}
	return rb.snapshot()
}

// Subscribe returns a channel that receives new messages for channel,
// and an unsubscribe function.
func (b *Bot) Subscribe(channel string) (<-chan Message, func()) {
	ch := make(chan Message, 64)

	b.mu.Lock()
	b.subSeq++
	id := b.subSeq
	if b.subs[channel] == nil {
		b.subs[channel] = make(map[uint64]chan Message)
	}
	b.subs[channel][id] = ch
	b.mu.Unlock()

	unsub := func() {
		b.mu.Lock()
		delete(b.subs[channel], id)
		b.mu.Unlock()
		close(ch)
	}
	return ch, unsub
}

// Send sends a message to channel. The message is attributed to senderNick
// via a visible prefix: "[senderNick] text". The sent message is also pushed
// directly into the buffer since IRC servers don't echo messages back to sender.
func (b *Bot) Send(ctx context.Context, channel, text, senderNick string) error {
	if b.client == nil {
		return fmt.Errorf("bridge: not connected")
	}
	ircText := text
	if senderNick != "" {
		ircText = "[" + senderNick + "] " + text
	}
	b.client.Cmd.Message(channel, ircText)

	// Track web sender as active in this channel.
	if senderNick != "" {
		b.TouchUser(channel, senderNick)
	}

	// Buffer the outgoing message immediately (server won't echo it back).
	// Use senderNick so the web UI shows who actually sent it.
	displayNick := b.nick
	if senderNick != "" {
		displayNick = senderNick
	}
	b.dispatch(Message{
		At:      time.Now(),
		Channel: channel,
		Nick:    displayNick,
		Text:    text,
	})
	return nil
}

// TouchUser marks a bridge/web nick as active in the given channel without
// sending a visible IRC message. This is used by broker-style local runtimes
// to maintain presence in the user list while idle.
func (b *Bot) TouchUser(channel, nick string) {
	if nick == "" {
		return
	}
	b.mu.Lock()
	if b.webUsers[channel] == nil {
		b.webUsers[channel] = make(map[string]time.Time)
	}
	b.webUsers[channel][nick] = time.Now()
	b.mu.Unlock()
}

// Users returns the current nick list for a channel — IRC connections plus
// web UI users who have posted recently within the configured TTL.
func (b *Bot) Users(channel string) []string {
	seen := make(map[string]bool)
	var nicks []string

	// IRC-connected nicks from NAMES — exclude the bridge bot itself.
	if b.client != nil {
		if ch := b.client.LookupChannel(channel); ch != nil {
			for _, u := range ch.Users(b.client) {
				if u.Nick == b.nick {
					continue // skip the bridge bot
				}
				if !seen[u.Nick] {
					seen[u.Nick] = true
					nicks = append(nicks, u.Nick)
				}
			}
		}
	}

	// Web UI senders active within the configured TTL. Also prune expired nicks
	// so the bridge doesn't retain dead web-user entries forever.
	now := time.Now()
	b.mu.Lock()
	cutoff := now.Add(-b.webUserTTL)
	for nick, last := range b.webUsers[channel] {
		if !last.After(cutoff) {
			delete(b.webUsers[channel], nick)
			continue
		}
		if !seen[nick] {
			seen[nick] = true
			nicks = append(nicks, nick)
		}
	}
	b.mu.Unlock()

	return nicks
}

// Stats returns a snapshot of bridge activity.
func (b *Bot) Stats() Stats {
	b.mu.RLock()
	channels := len(b.joined)
	subs := 0
	for _, m := range b.subs {
		subs += len(m)
	}
	b.mu.RUnlock()
	return Stats{
		Channels:      channels,
		MessagesTotal: b.msgTotal.Load(),
		ActiveSubs:    subs,
	}
}

// dispatch pushes a message to the ring buffer and fans out to subscribers.
func (b *Bot) dispatch(msg Message) {
	b.msgTotal.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	rb := b.buffers[msg.Channel]
	if rb == nil {
		return
	}
	rb.push(msg)
	for _, ch := range b.subs[msg.Channel] {
		select {
		case ch <- msg:
		default: // slow consumer, drop
		}
	}
}

// joinLoop reads from joinCh and joins channels on demand.
func (b *Bot) joinLoop(ctx context.Context, c *girc.Client) {
	for {
		select {
		case <-ctx.Done():
			return
		case ch := <-b.joinCh:
			b.mu.RLock()
			already := b.joined[ch]
			b.mu.RUnlock()
			if !already {
				c.Cmd.Join(ch)
			}
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
