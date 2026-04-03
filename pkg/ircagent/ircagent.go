package ircagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/conflicthq/scuttlebot/internal/llm"
	"github.com/lrstanley/girc"
)

const (
	defaultHistoryLen    = 20
	defaultTypingDelay   = 400 * time.Millisecond
	defaultErrorJoiner   = " - "
	defaultGatewayTimout = 60 * time.Second
)

var defaultActivityPrefixes = []string{"claude-", "codex-", "gemini-"}

// DefaultActivityPrefixes returns the default set of nick prefixes treated as
// status/activity senders rather than chat participants.
func DefaultActivityPrefixes() []string {
	return append([]string(nil), defaultActivityPrefixes...)
}

// Config configures the shared IRC agent runtime.
type Config struct {
	IRCAddr          string
	Nick             string
	Pass             string
	Channels         []string
	SystemPrompt     string
	Logger           *slog.Logger
	HistoryLen       int
	TypingDelay      time.Duration
	ErrorJoiner      string
	ActivityPrefixes []string
	Direct           *DirectConfig
	Gateway          *GatewayConfig
}

// DirectConfig configures direct provider mode.
type DirectConfig struct {
	Backend string
	APIKey  string
	Model   string
}

// GatewayConfig configures scuttlebot gateway mode.
type GatewayConfig struct {
	APIURL     string
	Token      string
	Backend    string
	HTTPClient *http.Client
}

type historyEntry struct {
	role    string
	nick    string
	content string
}

type completer interface {
	complete(ctx context.Context, prompt string) (string, error)
}

type directCompleter struct {
	provider llm.Provider
}

func (d *directCompleter) complete(ctx context.Context, prompt string) (string, error) {
	return d.provider.Summarize(ctx, prompt)
}

type gatewayCompleter struct {
	apiURL  string
	token   string
	backend string
	http    *http.Client
}

func (g *gatewayCompleter) complete(ctx context.Context, prompt string) (string, error) {
	body, _ := json.Marshal(map[string]string{"backend": g.backend, "prompt": prompt})
	req, err := http.NewRequestWithContext(ctx, "POST", g.apiURL+"/v1/llm/complete", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.token)

	resp, err := g.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("gateway request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway error %d: %s", resp.StatusCode, string(data))
	}

	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("gateway parse: %w", err)
	}
	return result.Text, nil
}

type agent struct {
	cfg     Config
	llm     completer
	log     *slog.Logger
	irc     *girc.Client
	mu      sync.Mutex
	history map[string][]historyEntry
}

// Run starts the IRC agent and blocks until the context is canceled or the IRC
// connection fails.
func Run(ctx context.Context, cfg Config) error {
	cfg = withDefaults(cfg)
	if err := validateConfig(cfg); err != nil {
		return err
	}

	llmClient, err := buildCompleter(cfg)
	if err != nil {
		return err
	}

	a := &agent{
		cfg:     cfg,
		llm:     llmClient,
		log:     cfg.Logger,
		history: make(map[string][]historyEntry),
	}
	return a.run(ctx)
}

// SplitCSV trims and splits comma-separated channel strings.
func SplitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func withDefaults(cfg Config) Config {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.HistoryLen <= 0 {
		cfg.HistoryLen = defaultHistoryLen
	}
	if cfg.TypingDelay <= 0 {
		cfg.TypingDelay = defaultTypingDelay
	}
	if cfg.ErrorJoiner == "" {
		cfg.ErrorJoiner = defaultErrorJoiner
	}
	if len(cfg.ActivityPrefixes) == 0 {
		cfg.ActivityPrefixes = append([]string(nil), defaultActivityPrefixes...)
	}
	if len(cfg.Channels) == 0 {
		cfg.Channels = []string{"#general"}
	}
	return cfg
}

func validateConfig(cfg Config) error {
	switch {
	case cfg.IRCAddr == "":
		return fmt.Errorf("irc address is required")
	case cfg.Nick == "":
		return fmt.Errorf("nick is required")
	case cfg.Pass == "":
		return fmt.Errorf("pass is required")
	case cfg.SystemPrompt == "":
		return fmt.Errorf("system prompt is required")
	}
	return nil
}

func buildCompleter(cfg Config) (completer, error) {
	gatewayConfigured := cfg.Gateway != nil && cfg.Gateway.Token != ""
	directConfigured := cfg.Direct != nil && cfg.Direct.APIKey != ""

	if gatewayConfigured && !directConfigured {
		if cfg.Gateway.APIURL == "" {
			return nil, fmt.Errorf("gateway api url is required")
		}
		if cfg.Gateway.Backend == "" {
			return nil, fmt.Errorf("gateway backend is required")
		}
		httpClient := cfg.Gateway.HTTPClient
		if httpClient == nil {
			httpClient = &http.Client{Timeout: defaultGatewayTimout}
		}
		cfg.Logger.Info("mode: gateway", "api-url", cfg.Gateway.APIURL, "backend", cfg.Gateway.Backend)
		return &gatewayCompleter{
			apiURL:  cfg.Gateway.APIURL,
			token:   cfg.Gateway.Token,
			backend: cfg.Gateway.Backend,
			http:    httpClient,
		}, nil
	}

	if directConfigured {
		if cfg.Direct.Backend == "" {
			return nil, fmt.Errorf("direct backend is required")
		}
		cfg.Logger.Info("mode: direct", "backend", cfg.Direct.Backend, "model", cfg.Direct.Model)
		provider, err := llm.New(llm.BackendConfig{
			Backend: cfg.Direct.Backend,
			APIKey:  cfg.Direct.APIKey,
			Model:   cfg.Direct.Model,
		})
		if err != nil {
			return nil, fmt.Errorf("build provider: %w", err)
		}
		return &directCompleter{provider: provider}, nil
	}

	return nil, fmt.Errorf("set gateway token or direct api key")
}

func (a *agent) run(ctx context.Context) error {
	host, port, err := splitHostPort(a.cfg.IRCAddr)
	if err != nil {
		return err
	}

	client := girc.New(girc.Config{
		Server: host,
		Port:   port,
		Nick:   a.cfg.Nick,
		User:   a.cfg.Nick,
		Name:   a.cfg.Nick + " (AI agent)",
		SASL:   &girc.SASLPlain{User: a.cfg.Nick, Pass: a.cfg.Pass},
	})

	client.Handlers.AddBg(girc.CONNECTED, func(cl *girc.Client, _ girc.Event) {
		a.log.Info("connected", "server", a.cfg.IRCAddr)
		for _, ch := range a.cfg.Channels {
			cl.Cmd.Join(ch)
		}
	})

	client.Handlers.AddBg(girc.PRIVMSG, func(cl *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil {
			return
		}

		target := e.Params[0]
		senderNick := e.Source.Name
		text := strings.TrimSpace(e.Last())
		if senderNick == a.cfg.Nick {
			return
		}

		if strings.HasPrefix(text, "[") {
			if end := strings.Index(text, "] "); end != -1 {
				senderNick = text[1:end]
				text = text[end+2:]
			}
		}

		isDM := !strings.HasPrefix(target, "#")
		isMentioned := MentionsNick(text, a.cfg.Nick)
		isActivityPost := HasAnyPrefix(senderNick, a.cfg.ActivityPrefixes)

		convKey := target
		if isDM {
			convKey = senderNick
		}
		a.appendHistory(convKey, "user", senderNick, text)

		if isActivityPost {
			return
		}
		if !isDM && !isMentioned {
			return
		}

		cleaned := TrimAddressedText(text, a.cfg.Nick)

		a.mu.Lock()
		history := a.history[convKey]
		if len(history) > 0 {
			history[len(history)-1].content = cleaned
			a.history[convKey] = history
		}
		a.mu.Unlock()

		replyTo := target
		if isDM {
			replyTo = senderNick
		}
		go a.respond(ctx, cl, convKey, replyTo, senderNick, isDM)
	})

	a.irc = client

	errCh := make(chan error, 1)
	go func() {
		if err := client.Connect(); err != nil && ctx.Err() == nil {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		client.Close()
		return nil
	case err := <-errCh:
		return fmt.Errorf("irc: %w", err)
	}
}

func (a *agent) respond(ctx context.Context, cl *girc.Client, convKey, replyTo, senderNick string, isDM bool) {
	prompt := a.buildPrompt(convKey)
	time.Sleep(a.cfg.TypingDelay)

	reply, err := a.llm.complete(ctx, prompt)
	if err != nil {
		a.log.Error("llm error", "err", err)
		cl.Cmd.Message(replyTo, senderNick+": sorry, something went wrong"+a.cfg.ErrorJoiner+err.Error())
		return
	}

	reply = strings.TrimSpace(reply)
	a.appendHistory(convKey, "assistant", a.cfg.Nick, reply)

	prefix := ""
	if !isDM && senderNick != "" {
		prefix = senderNick + ": "
	}
	for i, line := range strings.Split(reply, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if i == 0 {
			line = prefix + line
		}
		cl.Cmd.Message(replyTo, line)
	}
}

func (a *agent) buildPrompt(convKey string) string {
	a.mu.Lock()
	history := append([]historyEntry(nil), a.history[convKey]...)
	a.mu.Unlock()

	var sb strings.Builder
	sb.WriteString(a.cfg.SystemPrompt)
	sb.WriteString("\n\nConversation history:\n")
	for _, entry := range history {
		role := "User"
		if entry.role == "assistant" {
			role = "Assistant"
		}
		fmt.Fprintf(&sb, "[%s] %s: %s\n", role, entry.nick, entry.content)
	}
	sb.WriteString("\nRespond to the last user message. Be concise.")
	return sb.String()
}

func (a *agent) appendHistory(convKey, role, nick, content string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	history := a.history[convKey]
	history = append(history, historyEntry{role: role, nick: nick, content: content})
	if len(history) > a.cfg.HistoryLen {
		history = history[len(history)-a.cfg.HistoryLen:]
	}
	a.history[convKey] = history
}

// MentionsNick reports whether text contains a standalone mention of nick.
func MentionsNick(text, nick string) bool {
	lower := strings.ToLower(text)
	needle := strings.ToLower(nick)
	start := 0

	for {
		idx := strings.Index(lower[start:], needle)
		if idx < 0 {
			return false
		}
		idx += start

		before := idx == 0 || !isMentionAdjacent(lower[idx-1])
		after := idx+len(needle) >= len(lower) || !isMentionAdjacent(lower[idx+len(needle)])
		if before && after {
			return true
		}

		start = idx + 1
	}
}

// MatchesGroupMention checks if text contains a group mention that applies
// to an agent with the given nick and type. Supported patterns:
//
//   - @all — matches every agent
//   - @worker, @observer, @orchestrator, @operator — matches by agent type
//   - @prefix-* — matches agents whose nick starts with prefix- (e.g. @claude-* matches claude-kohakku-abc)
func MatchesGroupMention(text, nick, agentType string) bool {
	lower := strings.ToLower(text)

	// @all
	if containsWord(lower, "@all") {
		return true
	}

	// @role — e.g. @worker, @observer
	if agentType != "" && containsWord(lower, "@"+strings.ToLower(agentType)) {
		return true
	}

	// @prefix-* patterns — find all @word-* tokens in the text.
	for i := 0; i < len(lower); i++ {
		if lower[i] != '@' {
			continue
		}
		// Extract the token after @.
		j := i + 1
		for j < len(lower) && (isAlNum(lower[j]) || lower[j] == '*') {
			j++
		}
		token := lower[i+1 : j]
		if !strings.HasSuffix(token, "*") || len(token) < 2 {
			continue
		}
		prefix := token[:len(token)-1] // remove the *
		if strings.HasPrefix(strings.ToLower(nick), prefix) {
			return true
		}
	}

	return false
}

func containsWord(text, word string) bool {
	idx := strings.Index(text, word)
	if idx < 0 {
		return false
	}
	end := idx + len(word)
	before := idx == 0 || !isAlNum(text[idx-1])
	after := end >= len(text) || !isAlNum(text[end])
	return before && after
}

// TrimAddressedText removes an initial nick address from text when present.
func TrimAddressedText(text, nick string) string {
	cleaned := text
	lower := strings.ToLower(text)
	if idx := strings.Index(lower, strings.ToLower(nick)); idx != -1 {
		after := strings.TrimSpace(text[idx+len(nick):])
		after = strings.TrimLeft(after, ":, ")
		if after != "" {
			cleaned = after
		}
	}
	return cleaned
}

func isAlNum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_'
}

func isMentionAdjacent(c byte) bool {
	return isAlNum(c) || c == '.' || c == '/' || c == '\\'
}

// HasAnyPrefix reports whether s starts with any prefix in prefixes.
func HasAnyPrefix(s string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
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
