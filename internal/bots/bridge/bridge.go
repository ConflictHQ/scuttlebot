// Package bridge implements the IRC bridge bot for the web chat UI.
//
// The bridge connects to IRC, joins channels, and buffers recent messages.
// It exposes subscriptions for SSE fan-out and a Send method for the web UI
// to post messages back into IRC.
package bridge

import (
	"context"
	"encoding/json"
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

// Meta is optional structured metadata attached to a bridge message.
// IRC sees only the plain text; the web UI uses Meta for rich rendering.
type Meta struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Message is a single IRC message captured by the bridge.
type Message struct {
	At      time.Time `json:"at"`
	Channel string    `json:"channel"`
	Nick    string    `json:"nick"`
	Text    string    `json:"text"`
	MsgID   string    `json:"msgid,omitempty"`
	Meta    *Meta     `json:"meta,omitempty"`
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

	joinCh     chan string
	client     *girc.Client
	onUserJoin func(channel, nick string) // optional callback when a non-bridge user joins

	// namesUsers is our own authoritative user list populated from RPL_NAMREPLY.
	// channel → nick → mode prefix ("@", "+", or "")
	namesUsers map[string]map[string]string

	// RELAYMSG support detected from ISUPPORT.
	relaySep string // separator (e.g. "/"), empty if unsupported
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
		namesUsers:   make(map[string]map[string]string),
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

// SetOnUserJoin registers a callback invoked when a non-bridge user joins a channel.
func (b *Bot) SetOnUserJoin(fn func(channel, nick string)) {
	b.onUserJoin = fn
}

// Notice sends an IRC NOTICE to the given target (nick or channel).
func (b *Bot) Notice(target, text string) {
	if b.client != nil {
		b.client.Cmd.Notice(target, text)
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
		Server:      host,
		Port:        port,
		Nick:        b.nick,
		User:        b.nick,
		Name:        "scuttlebot bridge",
		SASL:        &girc.SASLPlain{User: b.nick, Pass: b.password},
		PingDelay:   30 * time.Second,
		PingTimeout: 30 * time.Second,
		SSL:         false,
		AllowFlood:  true, // trusted local connection — no rate limiting
	})

	c.Handlers.AddBg(girc.CONNECTED, func(cl *girc.Client, _ girc.Event) {
		cl.Cmd.Mode(cl.GetNick(), "+B")
		// RELAYMSG is an IRCv3 capability (draft/relaymsg), not an ISUPPORT token.
		// Check whether the server negotiated it during CAP exchange.
		if cl.HasCapability("draft/relaymsg") {
			b.relaySep = "/"
			if b.log != nil {
				b.log.Info("bridge: RELAYMSG supported", "separator", b.relaySep)
			}
		} else {
			b.relaySep = ""
			if b.log != nil {
				b.log.Info("bridge: RELAYMSG not supported, using [nick] prefix fallback")
			}
		}
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
		if len(e.Params) < 1 || e.Source == nil {
			return
		}
		channel := e.Params[0]
		nick := e.Source.Name

		if nick == b.nick {
			// Bridge itself joined — initialize buffers.
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
		} else if b.onUserJoin != nil {
			// Another user joined — fire callback for on-join instructions.
			go b.onUserJoin(channel, nick)
		}
	})

	// Parse RPL_NAMREPLY ourselves for a reliable user list.
	c.Handlers.AddBg(girc.RPL_NAMREPLY, func(_ *girc.Client, e girc.Event) {
		// Format: :server 353 bridge = #channel :@op +voice regular
		if len(e.Params) < 4 {
			return
		}
		channel := e.Params[2]
		names := strings.Fields(e.Last())
		b.mu.Lock()
		if b.namesUsers[channel] == nil {
			b.namesUsers[channel] = make(map[string]string)
		}
		for _, name := range names {
			prefix := ""
			nick := name
			if strings.HasPrefix(name, "@") {
				prefix = "@"
				nick = name[1:]
			} else if strings.HasPrefix(name, "+") {
				prefix = "+"
				nick = name[1:]
			}
			// Strip !user@host from userhost-in-names (IRCv3).
			if idx := strings.Index(nick, "!"); idx != -1 {
				nick = nick[:idx]
			}
			if nick != "" && nick != b.nick {
				b.namesUsers[channel][nick] = prefix
			}
		}
		b.mu.Unlock()
	})

	c.Handlers.AddBg(girc.PRIVMSG, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil {
			return
		}
		channel := e.Params[0]
		if !strings.HasPrefix(channel, "#") {
			return // ignore DMs
		}
		// Prefer account-tag (IRCv3) over source nick for sender identity.
		nick := e.Source.Name
		if acct, ok := e.Tags.Get("account"); ok && acct != "" {
			nick = acct
		}

		var msgID string
		if id, ok := e.Tags.Get("msgid"); ok {
			msgID = id
		}
		msg := Message{
			At:      e.Timestamp,
			Channel: channel,
			Nick:    nick,
			Text:    e.Last(),
			MsgID:   msgID,
		}
		// Read meta-type from IRCv3 client tags if present.
		if metaType, ok := e.Tags.Get("+scuttlebot/meta-type"); ok && metaType != "" {
			msg.Meta = &Meta{Type: metaType}
		}
		b.dispatch(msg)
	})

	b.client = c

	errCh := make(chan error, 1)
	go func() {
		if err := c.Connect(); err != nil && ctx.Err() == nil {
			errCh <- err
		}
	}()

	go b.joinLoop(ctx, c)
	go b.namesRefreshLoop(ctx)

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
	return b.SendWithMeta(ctx, channel, text, senderNick, nil)
}

// SendWithMeta sends a message to channel with optional structured metadata.
// IRC receives only the plain text; SSE subscribers receive the full message
// including meta for rich rendering in the web UI.
//
// When meta is present, key fields are attached as IRCv3 client-only tags
// (+scuttlebot/meta-type) so any IRCv3 client can read them.
//
// When the server supports RELAYMSG (IRCv3), messages are attributed natively
// so other clients see the real sender nick. Falls back to [nick] prefix.
func (b *Bot) SendWithMeta(ctx context.Context, channel, text, senderNick string, meta *Meta) error {
	if b.client == nil {
		return fmt.Errorf("bridge: not connected")
	}
	// Build optional IRCv3 tag prefix for meta-type.
	tagPrefix := ""
	if meta != nil && meta.Type != "" {
		tagPrefix = "@+scuttlebot/meta-type=" + meta.Type + " "
	}
	if senderNick != "" && b.relaySep != "" {
		// Use RELAYMSG for native attribution.
		_ = b.client.Cmd.SendRawf("%sRELAYMSG %s %s :%s", tagPrefix, channel, senderNick, text)
	} else {
		ircText := text
		if senderNick != "" {
			ircText = "[" + senderNick + "] " + text
		}
		if tagPrefix != "" {
			_ = b.client.Cmd.SendRawf("%sPRIVMSG %s :%s", tagPrefix, channel, ircText)
		} else {
			b.client.Cmd.Message(channel, ircText)
		}
	}

	if senderNick != "" {
		b.TouchUser(channel, senderNick)
	}

	displayNick := b.nick
	if senderNick != "" {
		displayNick = senderNick
	}
	b.dispatch(Message{
		At:      time.Now(),
		Channel: channel,
		Nick:    displayNick,
		Text:    text,
		Meta:    meta,
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

// RefreshNames sends a NAMES command for a channel, forcing girc to
// update its user list from the server's authoritative response.
func (b *Bot) RefreshNames(channel string) {
	if b.client != nil {
		_ = b.client.Cmd.SendRawf("NAMES %s", channel)
	}
}

// namesRefreshLoop periodically sends NAMES for all joined channels so
// girc's user tracking stays in sync with the server.
func (b *Bot) namesRefreshLoop(ctx context.Context) {
	// Wait for initial connection and bot joins to settle.
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.mu.RLock()
			channels := make([]string, 0, len(b.joined))
			for ch := range b.joined {
				channels = append(channels, ch)
			}
			b.mu.RUnlock()
			// Clear stale data before refresh.
			b.mu.Lock()
			for _, ch := range channels {
				b.namesUsers[ch] = make(map[string]string)
			}
			b.mu.Unlock()
			for _, ch := range channels {
				b.RefreshNames(ch)
			}
		}
	}
}

// Users returns the current nick list for a channel — from our NAMES cache
// plus web UI users who have posted recently within the configured TTL.
func (b *Bot) Users(channel string) []string {
	seen := make(map[string]bool)
	var nicks []string

	// IRC-connected nicks from our NAMES cache.
	b.mu.RLock()
	for nick := range b.namesUsers[channel] {
		if !seen[nick] {
			seen[nick] = true
			nicks = append(nicks, nick)
		}
	}
	b.mu.RUnlock()

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

// UserInfo describes a user with their IRC modes.
type UserInfo struct {
	Nick  string   `json:"nick"`
	Modes []string `json:"modes,omitempty"` // e.g. ["o", "v", "B"]
}

// UsersWithModes returns the current user list with mode info for a channel.
func (b *Bot) UsersWithModes(channel string) []UserInfo {
	seen := make(map[string]bool)
	var users []UserInfo

	// Use our NAMES cache for reliable user+mode data.
	b.mu.RLock()
	for nick, prefix := range b.namesUsers[channel] {
		if seen[nick] {
			continue
		}
		seen[nick] = true
		var modes []string
		if prefix == "@" {
			modes = append(modes, "o")
		} else if prefix == "+" {
			modes = append(modes, "v")
		}
		users = append(users, UserInfo{Nick: nick, Modes: modes})
	}
	b.mu.RUnlock()

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
			users = append(users, UserInfo{Nick: nick})
		}
	}
	b.mu.Unlock()

	return users
}

// ChannelModes returns the channel mode string (e.g. "+mnt") for a channel.
func (b *Bot) ChannelModes(channel string) string {
	if b.client == nil {
		return ""
	}
	ch := b.client.LookupChannel(channel)
	if ch == nil {
		return ""
	}
	return ch.Modes.String()
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
