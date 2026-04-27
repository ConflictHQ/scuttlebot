// Package sentinel implements the sentinel bot — an LLM-powered channel
// observer that detects policy violations and posts structured incident
// reports to a moderation channel.
//
// Sentinel never takes enforcement action. It watches, judges, and reports.
// All reports are human-readable and posted to a configured mod channel
// (e.g. #moderation) so the full audit trail is IRC-native and observable.
//
// Reports have the form:
//
//	[sentinel] incident in #channel | nick: <who> | severity: high | reason: <llm judgment>
package sentinel

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lrstanley/girc"

	"github.com/conflicthq/scuttlebot/internal/bots/cmdparse"
)

const defaultNick = "sentinel"

// LLMProvider calls a language model to evaluate channel content.
type LLMProvider interface {
	Summarize(ctx context.Context, prompt string) (string, error)
}

// Config controls sentinel's behaviour.
type Config struct {
	// IRCAddr is host:port of the Ergo IRC server.
	IRCAddr string
	// Nick is the IRC nick. Default: "sentinel".
	Nick string
	// Password is the SASL PLAIN passphrase.
	Password string

	// ModChannel is where incident reports are posted (e.g. "#moderation").
	ModChannel string
	// DMOperators, when true, also sends incident reports as DMs to AlertNicks.
	DMOperators bool
	// AlertNicks is the list of operator nicks to DM on incidents.
	AlertNicks []string

	// Policy is a plain-English description of what sentinel should flag.
	// Example: "Flag harassment, hate speech, spam, and coordinated manipulation."
	Policy string

	// WindowSize is how many messages to buffer per channel before analysis.
	// Default: 20.
	WindowSize int
	// WindowAge is the maximum age of buffered messages before a scan is forced.
	// Default: 5 minutes.
	WindowAge time.Duration
	// CooldownPerNick is the minimum time between reports about the same nick.
	// Default: 10 minutes.
	CooldownPerNick time.Duration
	// MinSeverity controls which severities trigger a report.
	// "low", "medium", "high" — default: "medium".
	MinSeverity string

	// Channels is the list of channels to join on connect.
	Channels []string
}

func (c *Config) setDefaults() {
	if c.Nick == "" {
		c.Nick = defaultNick
	}
	if c.WindowSize == 0 {
		c.WindowSize = 20
	}
	if c.WindowAge == 0 {
		c.WindowAge = 5 * time.Minute
	}
	if c.CooldownPerNick == 0 {
		c.CooldownPerNick = 10 * time.Minute
	}
	if c.MinSeverity == "" {
		c.MinSeverity = "medium"
	}
	if c.Policy == "" {
		c.Policy = "Flag harassment, hate speech, spam, threats, and coordinated manipulation."
	}
	if c.ModChannel == "" {
		c.ModChannel = "#moderation"
	}
}

// msgEntry is a buffered channel message.
type msgEntry struct {
	at   time.Time
	nick string
	text string
}

// chanBuffer holds unanalysed messages for a channel.
type chanBuffer struct {
	msgs     []msgEntry
	lastScan time.Time
}

// Bot is the sentinel bot.
type Bot struct {
	cfg    Config
	llm    LLMProvider
	log    *slog.Logger
	client *girc.Client

	mu          sync.Mutex
	buffers     map[string]*chanBuffer // channel → buffer
	cooldown    map[string]time.Time   // "channel:nick" → last report time
	analyseSlot chan struct{}          // bounded-concurrency semaphore for analyse goroutines (#175)

	recent []incidentRecord // ring buffer of recent reports (capped at maxRecent)
}

// incidentRecord captures a posted incident for STATUS/REPORT command output.
type incidentRecord struct {
	at       time.Time
	channel  string
	nick     string
	severity string
	reason   string
}

const maxRecentIncidents = 50

// maxConcurrentAnalyses caps simultaneous LLM analysis goroutines so a busy
// fleet doesn't fan out N×M concurrent provider calls and rate-limit itself.
const maxConcurrentAnalyses = 4

// New creates a sentinel Bot.
func New(cfg Config, llm LLMProvider, log *slog.Logger) *Bot {
	cfg.setDefaults()
	return &Bot{
		cfg:         cfg,
		llm:         llm,
		log:         log,
		buffers:     make(map[string]*chanBuffer),
		cooldown:    make(map[string]time.Time),
		analyseSlot: make(chan struct{}, maxConcurrentAnalyses),
	}
}

// Start connects to IRC and begins observation. Blocks until ctx is done.
func (b *Bot) Start(ctx context.Context) error {
	host, port, err := splitHostPort(b.cfg.IRCAddr)
	if err != nil {
		return fmt.Errorf("sentinel: %w", err)
	}

	c := girc.New(girc.Config{
		Server:      host,
		Port:        port,
		Nick:        b.cfg.Nick,
		User:        b.cfg.Nick,
		Name:        "scuttlebot sentinel",
		SASL:        &girc.SASLPlain{User: b.cfg.Nick, Pass: b.cfg.Password},
		PingDelay:   30 * time.Second,
		PingTimeout: 30 * time.Second,
	})

	c.Handlers.AddBg(girc.CONNECTED, func(cl *girc.Client, _ girc.Event) {
		cl.Cmd.Mode(cl.GetNick(), "+B")
		for _, ch := range b.cfg.Channels {
			cl.Cmd.Join(ch)
		}
		cl.Cmd.Join(b.cfg.ModChannel)
		if b.log != nil {
			b.log.Info("sentinel connected", "channels", b.cfg.Channels)
		}
	})

	c.Handlers.AddBg(girc.INVITE, func(cl *girc.Client, e girc.Event) {
		if ch := e.Last(); strings.HasPrefix(ch, "#") {
			cl.Cmd.Join(ch)
		}
	})

	router := cmdparse.NewRouter(b.cfg.Nick)
	router.SetPurpose("the LLM-powered policy observer — watches channels and reports incidents to the mod channel")
	router.Register(cmdparse.Command{
		Name:        "report",
		Usage:       "REPORT [#channel]",
		Description: "force an LLM review of the current message buffer",
		Handler: func(cmdCtx *cmdparse.Context, args string) string {
			return b.cmdReport(ctx, cmdCtx, args)
		},
	})
	router.Register(cmdparse.Command{
		Name:        "status",
		Usage:       "STATUS",
		Description: "show watched channels, buffer state, and recent incident count",
		Handler: func(_ *cmdparse.Context, _ string) string {
			return b.cmdStatus()
		},
	})
	router.Register(cmdparse.Command{
		Name:        "recent",
		Usage:       "RECENT [N]",
		Description: "list the most recent incidents",
		Handler: func(_ *cmdparse.Context, args string) string {
			return b.cmdRecent(args)
		},
	})
	router.Register(cmdparse.Command{
		Name:        "dismiss",
		Usage:       "DISMISS <nick> [#channel]",
		Description: "clear cooldown for nick so future violations report immediately",
		Handler: func(cmdCtx *cmdparse.Context, args string) string {
			return b.cmdDismiss(cmdCtx, args)
		},
	})

	c.Handlers.AddBg(girc.PRIVMSG, func(cl *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil {
			return
		}
		// Dispatch commands (DMs and channel messages).
		if reply := router.Dispatch(e.Source.Name, e.Params[0], e.Last()); reply != nil {
			for _, line := range reply.Lines() {
				cl.Cmd.Message(reply.Target, line)
			}
			return
		}
		channel := e.Params[0]
		if !strings.HasPrefix(channel, "#") {
			return // non-command DMs ignored
		}
		if channel == b.cfg.ModChannel {
			return // don't analyse the mod channel itself
		}
		nick := e.Source.Name
		if nick == b.cfg.Nick {
			return
		}
		b.buffer(ctx, channel, nick, e.Last())
	})

	b.mu.Lock()
	b.client = c
	b.mu.Unlock()

	// Background scanner — forces analysis on aged buffers.
	go b.scanLoop(ctx)

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
		return fmt.Errorf("sentinel: irc: %w", err)
	}
}

// JoinChannel joins an additional channel.
func (b *Bot) JoinChannel(channel string) {
	if b.client != nil {
		b.client.Cmd.Join(channel)
	}
}

// buffer appends a message to the channel buffer and triggers analysis
// when the window is full.
func (b *Bot) buffer(ctx context.Context, channel, nick, text string) {
	b.mu.Lock()
	buf := b.buffers[channel]
	if buf == nil {
		buf = &chanBuffer{lastScan: time.Now()}
		b.buffers[channel] = buf
	}
	buf.msgs = append(buf.msgs, msgEntry{at: time.Now(), nick: nick, text: text})
	ready := len(buf.msgs) >= b.cfg.WindowSize
	if ready {
		msgs := buf.msgs
		buf.msgs = nil
		buf.lastScan = time.Now()
		b.mu.Unlock()
		go b.analyse(ctx, channel, msgs)
	} else {
		b.mu.Unlock()
	}
}

// scanLoop forces analysis of stale buffers periodically.
func (b *Bot) scanLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.flushStale(ctx)
		}
	}
}

func (b *Bot) flushStale(ctx context.Context) {
	b.mu.Lock()
	var work []struct {
		channel string
		msgs    []msgEntry
	}
	for ch, buf := range b.buffers {
		if len(buf.msgs) == 0 {
			continue
		}
		if time.Since(buf.lastScan) >= b.cfg.WindowAge {
			work = append(work, struct {
				channel string
				msgs    []msgEntry
			}{ch, buf.msgs})
			buf.msgs = nil
			buf.lastScan = time.Now()
		}
	}
	b.mu.Unlock()
	for _, w := range work {
		go b.analyse(ctx, w.channel, w.msgs)
	}
}

// analyse sends a window of messages to the LLM and reports any violations.
// Concurrency-capped via analyseSlot to prevent N×M parallel provider calls
// when many channel windows fill simultaneously (#175).
func (b *Bot) analyse(ctx context.Context, channel string, msgs []msgEntry) {
	if b.llm == nil || len(msgs) == 0 {
		return
	}
	// Acquire a slot — blocks if maxConcurrentAnalyses are already in flight.
	select {
	case b.analyseSlot <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-b.analyseSlot }()

	prompt := b.buildPrompt(channel, msgs)
	result, err := b.llm.Summarize(ctx, prompt)
	if err != nil {
		if b.log != nil {
			b.log.Error("sentinel: llm error", "channel", channel, "err", err)
		}
		return
	}

	b.parseAndReport(channel, result)
}

// buildPrompt constructs the LLM prompt for a message window.
func (b *Bot) buildPrompt(channel string, msgs []msgEntry) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are a channel moderation assistant. Your policy:\n%s\n\n", b.cfg.Policy)
	fmt.Fprintf(&sb, "Review the following IRC messages from %s and identify any policy violations.\n", channel)
	fmt.Fprintf(&sb, "For each violation found, respond with one line in this exact format:\n")
	fmt.Fprintf(&sb, "INCIDENT | nick: <nick> | severity: low|medium|high | reason: <brief reason>\n\n")
	fmt.Fprintf(&sb, "If there are no violations, respond with: CLEAN\n\n")
	fmt.Fprintf(&sb, "Messages (%d):\n", len(msgs))
	for _, m := range msgs {
		fmt.Fprintf(&sb, "[%s] %s: %s\n", m.at.Format("15:04:05"), m.nick, m.text)
	}
	return sb.String()
}

// parseAndReport parses LLM output and posts reports to the mod channel.
func (b *Bot) parseAndReport(channel, result string) {
	if b.client == nil {
		return
	}
	lines := strings.Split(strings.TrimSpace(result), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.EqualFold(line, "CLEAN") {
			continue
		}
		if !strings.HasPrefix(strings.ToUpper(line), "INCIDENT") {
			continue
		}

		nick, severity, reason := parseIncidentLine(line)
		if !b.severityMeetsMin(severity) {
			continue
		}

		// Cooldown check.
		coolKey := channel + ":" + nick
		b.mu.Lock()
		if last, ok := b.cooldown[coolKey]; ok && time.Since(last) < b.cfg.CooldownPerNick {
			b.mu.Unlock()
			continue
		}
		b.cooldown[coolKey] = time.Now()
		pruneTimeMap(b.cooldown, 24*time.Hour) // bound the map (#175)
		b.mu.Unlock()

		report := fmt.Sprintf("[sentinel] incident in %s | nick: %s | severity: %s | reason: %s",
			channel, nick, severity, reason)

		if b.log != nil {
			b.log.Warn("sentinel incident", "channel", channel, "nick", nick, "severity", severity, "reason", reason)
		}
		b.recordIncident(channel, nick, severity, reason)
		b.client.Cmd.Message(b.cfg.ModChannel, report)
		if b.cfg.DMOperators {
			for _, nick := range b.cfg.AlertNicks {
				b.client.Cmd.Message(nick, report)
			}
		}
	}
}

func parseIncidentLine(line string) (nick, severity, reason string) {
	// Format: INCIDENT | nick: X | severity: Y | reason: Z
	parts := strings.Split(line, "|")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if kv, ok := strings.CutPrefix(p, "nick:"); ok {
			nick = strings.TrimSpace(kv)
		} else if kv, ok := strings.CutPrefix(p, "severity:"); ok {
			severity = strings.ToLower(strings.TrimSpace(kv))
		} else if kv, ok := strings.CutPrefix(p, "reason:"); ok {
			reason = strings.TrimSpace(kv)
		}
	}
	if nick == "" {
		nick = "unknown"
	}
	if severity == "" {
		severity = "medium"
	}
	return
}

func (b *Bot) severityMeetsMin(severity string) bool {
	order := map[string]int{"low": 0, "medium": 1, "high": 2}
	min := order[b.cfg.MinSeverity]
	got, ok := order[severity]
	if !ok {
		return true // unknown severity — report it
	}
	return got >= min
}

// pruneTimeMap drops keys whose timestamp is older than maxAge. Keeps the
// cooldown map bounded over long-running deployments (#175).
// Caller must hold the appropriate lock.
func pruneTimeMap(m map[string]time.Time, maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	for k, t := range m {
		if t.Before(cutoff) {
			delete(m, k)
		}
	}
}

// recordIncident appends to the recent ring buffer, trimming to maxRecent.
func (b *Bot) recordIncident(channel, nick, severity, reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recent = append(b.recent, incidentRecord{
		at: time.Now(), channel: channel, nick: nick, severity: severity, reason: reason,
	})
	if len(b.recent) > maxRecentIncidents {
		b.recent = b.recent[len(b.recent)-maxRecentIncidents:]
	}
}

func (b *Bot) cmdReport(ctx context.Context, cmdCtx *cmdparse.Context, args string) string {
	channel := strings.TrimSpace(args)
	if channel == "" {
		channel = cmdCtx.Channel
	}
	if channel == "" {
		return "sentinel: name a channel — usage: REPORT [#channel]"
	}
	b.mu.Lock()
	buf := b.buffers[channel]
	if buf == nil || len(buf.msgs) == 0 {
		b.mu.Unlock()
		return fmt.Sprintf("sentinel: no messages buffered for %s yet", channel)
	}
	msgs := buf.msgs
	buf.msgs = nil
	buf.lastScan = time.Now()
	b.mu.Unlock()
	go b.analyse(ctx, channel, msgs)
	return fmt.Sprintf("sentinel: forced review of %d message(s) from %s — incidents (if any) will land in %s",
		len(msgs), channel, b.cfg.ModChannel)
}

func (b *Bot) cmdStatus() string {
	b.mu.Lock()
	channels := make([]string, 0, len(b.buffers))
	for ch := range b.buffers {
		channels = append(channels, ch)
	}
	bufSizes := make(map[string]int, len(channels))
	for ch, buf := range b.buffers {
		bufSizes[ch] = len(buf.msgs)
	}
	recent := len(b.recent)
	cooldowns := len(b.cooldown)
	b.mu.Unlock()
	sort.Strings(channels)

	inflight := len(b.analyseSlot)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("sentinel: mod=%s | min severity=%s | window=%d/age=%s | analyses in-flight %d/%d\n",
		b.cfg.ModChannel, b.cfg.MinSeverity, b.cfg.WindowSize, b.cfg.WindowAge.Round(time.Second),
		inflight, maxConcurrentAnalyses))
	if len(channels) == 0 {
		sb.WriteString("watching: (no channels yet)")
	} else {
		sb.WriteString("watching:")
		for _, ch := range channels {
			sb.WriteString(fmt.Sprintf(" %s(%d)", ch, bufSizes[ch]))
		}
	}
	sb.WriteString(fmt.Sprintf("\nrecent incidents: %d | cooldowns active: %d", recent, cooldowns))
	return sb.String()
}

func (b *Bot) cmdRecent(args string) string {
	limit := 5
	if s := strings.TrimSpace(args); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= maxRecentIncidents {
			limit = n
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.recent) == 0 {
		return "sentinel: no incidents reported yet"
	}
	start := 0
	if len(b.recent) > limit {
		start = len(b.recent) - limit
	}
	tail := b.recent[start:]

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("sentinel: %d most recent incident(s):\n", len(tail)))
	for _, inc := range tail {
		sb.WriteString(fmt.Sprintf("  %s %s in %s | %s | %s — %s\n",
			inc.at.Format("15:04:05"), inc.nick, inc.channel, inc.severity, "", inc.reason))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (b *Bot) cmdDismiss(cmdCtx *cmdparse.Context, args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return "sentinel: usage — DISMISS <nick> [#channel]"
	}
	fields := strings.Fields(args)
	nick := fields[0]
	channel := cmdCtx.Channel
	if len(fields) > 1 && strings.HasPrefix(fields[1], "#") {
		channel = fields[1]
	}
	if channel == "" {
		return "sentinel: in a DM you must name a #channel — usage: DISMISS <nick> #channel"
	}
	key := channel + ":" + nick
	b.mu.Lock()
	_, had := b.cooldown[key]
	delete(b.cooldown, key)
	b.mu.Unlock()
	if !had {
		return fmt.Sprintf("sentinel: no active cooldown for %s in %s", nick, channel)
	}
	return fmt.Sprintf("sentinel: cleared cooldown for %s in %s", nick, channel)
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
