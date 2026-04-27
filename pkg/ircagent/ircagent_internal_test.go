package ircagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDefaultActivityPrefixes(t *testing.T) {
	got := DefaultActivityPrefixes()
	want := []string{"claude-", "codex-", "gemini-"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Mutating the returned slice must not affect package-level state.
	got[0] = "mutated-"
	again := DefaultActivityPrefixes()
	if again[0] != "claude-" {
		t.Fatalf("DefaultActivityPrefixes returned slice that aliases package state: %q", again[0])
	}
	if defaultActivityPrefixes[0] != "claude-" {
		t.Fatalf("internal default prefixes were mutated: %q", defaultActivityPrefixes[0])
	}
}

func TestWithDefaults(t *testing.T) {
	cfg := withDefaults(Config{})
	if cfg.Logger == nil {
		t.Error("Logger should default to non-nil")
	}
	if cfg.HistoryLen != defaultHistoryLen {
		t.Errorf("HistoryLen = %d, want %d", cfg.HistoryLen, defaultHistoryLen)
	}
	if cfg.TypingDelay != defaultTypingDelay {
		t.Errorf("TypingDelay = %s, want %s", cfg.TypingDelay, defaultTypingDelay)
	}
	if cfg.ErrorJoiner != defaultErrorJoiner {
		t.Errorf("ErrorJoiner = %q, want %q", cfg.ErrorJoiner, defaultErrorJoiner)
	}
	if len(cfg.ActivityPrefixes) != len(defaultActivityPrefixes) {
		t.Errorf("ActivityPrefixes len = %d, want %d", len(cfg.ActivityPrefixes), len(defaultActivityPrefixes))
	}
	if len(cfg.Channels) != 1 || cfg.Channels[0] != "#general" {
		t.Errorf("Channels = %v, want [#general]", cfg.Channels)
	}

	// Mutating cfg.ActivityPrefixes must not corrupt the package default.
	cfg.ActivityPrefixes[0] = "evil-"
	if defaultActivityPrefixes[0] != "claude-" {
		t.Fatalf("withDefaults aliased defaultActivityPrefixes; saw %q", defaultActivityPrefixes[0])
	}

	// Pre-populated values should pass through untouched.
	custom := withDefaults(Config{
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		HistoryLen:       7,
		TypingDelay:      50 * time.Millisecond,
		ErrorJoiner:      " | ",
		ActivityPrefixes: []string{"x-"},
		Channels:         []string{"#a", "#b"},
	})
	if custom.HistoryLen != 7 || custom.TypingDelay != 50*time.Millisecond {
		t.Errorf("custom values overridden: %+v", custom)
	}
	if custom.ErrorJoiner != " | " || custom.ActivityPrefixes[0] != "x-" {
		t.Errorf("custom values overridden: %+v", custom)
	}
	if len(custom.Channels) != 2 {
		t.Errorf("custom channels overridden: %v", custom.Channels)
	}
}

func TestValidateConfig(t *testing.T) {
	base := Config{
		IRCAddr:      "localhost:6667",
		Nick:         "n",
		Pass:         "p",
		SystemPrompt: "be helpful",
	}
	if err := validateConfig(base); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}

	cases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"missing addr", func(c *Config) { c.IRCAddr = "" }, "irc address"},
		{"missing nick", func(c *Config) { c.Nick = "" }, "nick"},
		{"missing pass", func(c *Config) { c.Pass = "" }, "pass"},
		{"missing prompt", func(c *Config) { c.SystemPrompt = "" }, "system prompt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mut(&cfg)
			err := validateConfig(cfg)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestSplitHostPort(t *testing.T) {
	host, port, err := splitHostPort("irc.example.com:6667")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if host != "irc.example.com" || port != 6667 {
		t.Errorf("got %q:%d", host, port)
	}

	if _, _, err := splitHostPort("noport"); err == nil {
		t.Error("expected error for missing port")
	}
	if _, _, err := splitHostPort("host:notanint"); err == nil {
		t.Error("expected error for non-integer port")
	}
}

func TestBuildCompleterErrors(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Neither gateway nor direct configured.
	if _, err := buildCompleter(Config{Logger: logger}); err == nil {
		t.Error("expected error when neither gateway nor direct configured")
	}

	// Gateway with token but no API URL.
	_, err := buildCompleter(Config{
		Logger:  logger,
		Gateway: &GatewayConfig{Token: "t"},
	})
	if err == nil || !strings.Contains(err.Error(), "api url") {
		t.Errorf("expected api url error, got %v", err)
	}

	// Gateway with token + URL but no backend.
	_, err = buildCompleter(Config{
		Logger:  logger,
		Gateway: &GatewayConfig{Token: "t", APIURL: "http://x"},
	})
	if err == nil || !strings.Contains(err.Error(), "backend") {
		t.Errorf("expected backend error, got %v", err)
	}

	// Direct with key but no backend.
	_, err = buildCompleter(Config{
		Logger: logger,
		Direct: &DirectConfig{APIKey: "k"},
	})
	if err == nil || !strings.Contains(err.Error(), "backend") {
		t.Errorf("expected direct backend error, got %v", err)
	}

	// Direct with unknown backend (provider build fails).
	_, err = buildCompleter(Config{
		Logger: logger,
		Direct: &DirectConfig{APIKey: "k", Backend: "totally-not-a-backend"},
	})
	if err == nil || !strings.Contains(err.Error(), "build provider") {
		t.Errorf("expected build provider error, got %v", err)
	}
}

func TestBuildCompleterGatewaySuccess(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c, err := buildCompleter(Config{
		Logger: logger,
		Gateway: &GatewayConfig{
			Token:   "t",
			APIURL:  "http://gateway.local",
			Backend: "openai",
		},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	gc, ok := c.(*gatewayCompleter)
	if !ok {
		t.Fatalf("got %T, want *gatewayCompleter", c)
	}
	if gc.http == nil {
		t.Error("http client must be defaulted")
	}
	if gc.token != "t" || gc.apiURL != "http://gateway.local" || gc.backend != "openai" {
		t.Errorf("unexpected gateway state: %+v", gc)
	}

	// Caller-supplied http.Client should be used verbatim.
	custom := &http.Client{Timeout: 1 * time.Second}
	c2, err := buildCompleter(Config{
		Logger: logger,
		Gateway: &GatewayConfig{
			Token: "t", APIURL: "http://gw", Backend: "openai", HTTPClient: custom,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c2.(*gatewayCompleter).http != custom {
		t.Error("custom http client not used")
	}
}

func TestBuildCompleterDirectSuccess(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// "openai" requires API key + base URL; with a key set it should construct.
	c, err := buildCompleter(Config{
		Logger: logger,
		Direct: &DirectConfig{Backend: "openai", APIKey: "k", Model: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if _, ok := c.(*directCompleter); !ok {
		t.Fatalf("got %T, want *directCompleter", c)
	}
}

func TestBuildCompleterPrefersDirectOverGateway(t *testing.T) {
	// When both are configured, direct mode wins (current implementation).
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c, err := buildCompleter(Config{
		Logger:  logger,
		Direct:  &DirectConfig{Backend: "openai", APIKey: "k"},
		Gateway: &GatewayConfig{Token: "t", APIURL: "http://x", Backend: "openai"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.(*directCompleter); !ok {
		t.Fatalf("expected direct completer when both configured, got %T", c)
	}
}

func TestGatewayCompleterComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/llm/complete" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer my-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "wrong ct", http.StatusBadRequest)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body["backend"] != "openai" || body["prompt"] != "say hi" {
			http.Error(w, "wrong body", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "hello"})
	}))
	defer srv.Close()

	gc := &gatewayCompleter{
		apiURL:  srv.URL,
		token:   "my-token",
		backend: "openai",
		http:    srv.Client(),
	}
	got, err := gc.complete(context.Background(), "say hi")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestGatewayCompleterCompleteHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	gc := &gatewayCompleter{apiURL: srv.URL, token: "t", backend: "openai", http: srv.Client()}
	_, err := gc.complete(context.Background(), "anything")
	if err == nil || !strings.Contains(err.Error(), "gateway error 500") {
		t.Fatalf("expected gateway 500 error, got %v", err)
	}
}

func TestGatewayCompleterCompleteBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	gc := &gatewayCompleter{apiURL: srv.URL, token: "t", backend: "openai", http: srv.Client()}
	_, err := gc.complete(context.Background(), "anything")
	if err == nil || !strings.Contains(err.Error(), "gateway parse") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestGatewayCompleterCompleteRequestError(t *testing.T) {
	gc := &gatewayCompleter{
		// Use a clearly bogus host that fails immediately.
		apiURL:  "http://127.0.0.1:1",
		token:   "t",
		backend: "openai",
		http:    &http.Client{Timeout: 100 * time.Millisecond},
	}
	_, err := gc.complete(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error from unreachable gateway")
	}
	if !strings.Contains(err.Error(), "gateway request") {
		t.Errorf("expected 'gateway request' wrap, got %v", err)
	}
}

func TestGatewayCompleterBadURL(t *testing.T) {
	// http.NewRequestWithContext rejects a URL with a control character in scheme.
	gc := &gatewayCompleter{
		apiURL:  "://broken",
		token:   "t",
		backend: "openai",
		http:    http.DefaultClient,
	}
	_, err := gc.complete(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error building request")
	}
}

// stubProvider satisfies llm.Provider for directCompleter tests.
type stubProvider struct {
	resp string
	err  error
}

func (s *stubProvider) Summarize(_ context.Context, _ string) (string, error) {
	return s.resp, s.err
}

func TestDirectCompleterDelegates(t *testing.T) {
	dc := &directCompleter{provider: &stubProvider{resp: "ok"}}
	got, err := dc.complete(context.Background(), "p")
	if err != nil || got != "ok" {
		t.Errorf("got %q, %v", got, err)
	}

	dc = &directCompleter{provider: &stubProvider{err: errors.New("fail")}}
	if _, err := dc.complete(context.Background(), "p"); err == nil {
		t.Error("expected provider error to surface")
	}
}

func TestAgentAppendHistoryAndPrompt(t *testing.T) {
	a := &agent{
		cfg: Config{
			SystemPrompt: "you are helpful",
			HistoryLen:   3,
		},
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		history: make(map[string][]historyEntry),
	}

	a.appendHistory("#fleet", "user", "alice", "first")
	a.appendHistory("#fleet", "assistant", "bot", "second")
	a.appendHistory("#fleet", "user", "alice", "third")
	a.appendHistory("#fleet", "user", "alice", "fourth") // forces eviction of "first"

	if len(a.history["#fleet"]) != 3 {
		t.Fatalf("len = %d, want 3 (HistoryLen cap)", len(a.history["#fleet"]))
	}
	if a.history["#fleet"][0].content != "second" {
		t.Errorf("oldest = %q, want %q", a.history["#fleet"][0].content, "second")
	}

	prompt := a.buildPrompt("#fleet")
	if !strings.Contains(prompt, "you are helpful") {
		t.Error("prompt missing system prompt")
	}
	if !strings.Contains(prompt, "[User] alice: fourth") {
		t.Errorf("prompt missing latest user line; got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "[Assistant] bot: second") {
		t.Errorf("prompt missing assistant line; got:\n%s", prompt)
	}
	if strings.Contains(prompt, "first") {
		t.Errorf("prompt still contains evicted history; got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Conversation history:") {
		t.Error("prompt missing 'Conversation history:' header")
	}
	if !strings.HasSuffix(strings.TrimSpace(prompt), "Be concise.") {
		t.Errorf("prompt does not end with concise instruction; got:\n%s", prompt)
	}
}

func TestAgentBuildPromptEmptyHistory(t *testing.T) {
	a := &agent{
		cfg:     Config{SystemPrompt: "be brief", HistoryLen: 5},
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		history: make(map[string][]historyEntry),
	}
	prompt := a.buildPrompt("nobody")
	if !strings.Contains(prompt, "be brief") {
		t.Error("system prompt missing")
	}
	// Buffer used internally — sanity check it isn't empty.
	if len(strings.TrimSpace(prompt)) == 0 {
		t.Error("empty prompt")
	}
	// Round-trip through bytes.Buffer to ensure no panic on empty history.
	var buf bytes.Buffer
	buf.WriteString(prompt)
}

func TestRunValidatesConfig(t *testing.T) {
	// Run should reject invalid config without ever connecting.
	err := Run(context.Background(), Config{})
	if err == nil {
		t.Fatal("expected error for empty config")
	}
}

func TestRunRejectsMissingCompleter(t *testing.T) {
	// Valid IRC bits, but no LLM configured at all.
	err := Run(context.Background(), Config{
		IRCAddr:      "localhost:6667",
		Nick:         "n",
		Pass:         "p",
		SystemPrompt: "s",
	})
	if err == nil {
		t.Fatal("expected error when no completer configured")
	}
	if !strings.Contains(err.Error(), "gateway token or direct api key") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunBadAddress(t *testing.T) {
	// Pass validation and completer build, but fail to parse the address.
	err := Run(context.Background(), Config{
		IRCAddr:      "no-port-here",
		Nick:         "n",
		Pass:         "p",
		SystemPrompt: "s",
		Direct:       &DirectConfig{Backend: "openai", APIKey: "k"},
	})
	if err == nil {
		t.Fatal("expected error parsing address")
	}
}

func TestRunUnreachableAddress(t *testing.T) {
	// Use 127.0.0.1:1 (well-known port nothing should be listening on) so
	// girc returns a connection-refused error; this exercises the errCh path
	// in agent.run without triggering the girc internal race that the tcp-
	// accept-and-hold pattern surfaces.
	err := Run(context.Background(), Config{
		IRCAddr:      "127.0.0.1:1",
		Nick:         "n",
		Pass:         "p",
		SystemPrompt: "s",
		Channels:     []string{"#x"},
		Direct:       &DirectConfig{Backend: "openai", APIKey: "k"},
	})
	if err == nil {
		t.Fatal("expected connection error")
	}
	if !strings.Contains(err.Error(), "irc:") {
		t.Errorf("expected irc-wrapped error, got %v", err)
	}
}
