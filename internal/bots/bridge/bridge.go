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
	"time"

	"github.com/lrstanley/girc"
)

const botNick = "bridge"

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

	joinCh chan string
	client *girc.Client
}

// New creates a bridge Bot.
func New(ircAddr, nick, password string, channels []string, bufSize int, log *slog.Logger) *Bot {
	if nick == "" {
		nick = botNick
	}
	if bufSize <= 0 {
		bufSize = 200
	}
	return &Bot{
		ircAddr:      ircAddr,
		nick:         nick,
		password:     password,
		bufSize:      bufSize,
		initChannels: channels,
		log:          log,
		buffers:      make(map[string]*ringBuf),
		subs:         make(map[string]map[uint64]chan Message),
		joined:       make(map[string]bool),
		joinCh:       make(chan string, 32),
	}
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
		Server: host,
		Port:   port,
		Nick:   b.nick,
		User:   b.nick,
		Name:   "scuttlebot bridge",
		SASL:   &girc.SASLPlain{User: b.nick, Pass: b.password},
		SSL:    false,
	})

	c.Handlers.AddBg(girc.CONNECTED, func(cl *girc.Client, _ girc.Event) {
		if b.log != nil {
			b.log.Info("bridge connected")
		}
		for _, ch := range b.initChannels {
			cl.Cmd.Join(ch)
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
	// Buffer the outgoing message immediately (server won't echo it back).
	b.dispatch(Message{
		At:      time.Now(),
		Channel: channel,
		Nick:    b.nick,
		Text:    ircText,
	})
	return nil
}

// dispatch pushes a message to the ring buffer and fans out to subscribers.
func (b *Bot) dispatch(msg Message) {
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
