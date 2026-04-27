package sentinel_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/internal/bots/sentinel"
)

// fakeLLM records prompts and returns canned responses. Thread-safe: analyse
// may run on a goroutine.
type fakeLLM struct {
	mu      sync.Mutex
	prompts []string
	resp    string
	err     error
}

func (f *fakeLLM) Summarize(_ context.Context, prompt string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, prompt)
	return f.resp, f.err
}

func (f *fakeLLM) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prompts)
}

func (f *fakeLLM) lastPrompt() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.prompts) == 0 {
		return ""
	}
	return f.prompts[len(f.prompts)-1]
}

// --- parseIncidentLine ------------------------------------------------------

func TestParseIncidentLineFullyQualified(t *testing.T) {
	line := "INCIDENT | nick: alice | severity: high | reason: posted slurs"
	nick, sev, reason := sentinel.ParseIncidentLineForTest(line)
	if nick != "alice" {
		t.Errorf("nick: %q", nick)
	}
	if sev != "high" {
		t.Errorf("severity: %q", sev)
	}
	if reason != "posted slurs" {
		t.Errorf("reason: %q", reason)
	}
}

func TestParseIncidentLineMissingNickFallsBackToUnknown(t *testing.T) {
	nick, sev, _ := sentinel.ParseIncidentLineForTest("INCIDENT | severity: low | reason: minor")
	if nick != "unknown" {
		t.Errorf("nick: %q want unknown", nick)
	}
	if sev != "low" {
		t.Errorf("severity: %q", sev)
	}
}

func TestParseIncidentLineMissingSeverityDefaultsMedium(t *testing.T) {
	_, sev, _ := sentinel.ParseIncidentLineForTest("INCIDENT | nick: bob | reason: noise")
	if sev != "medium" {
		t.Errorf("default severity: %q want medium", sev)
	}
}

func TestParseIncidentLineNormalisesSeverityCase(t *testing.T) {
	_, sev, _ := sentinel.ParseIncidentLineForTest("INCIDENT | nick: x | severity: HIGH")
	if sev != "high" {
		t.Errorf("severity should be lower-cased: %q", sev)
	}
}

func TestParseIncidentLineTrimsWhitespace(t *testing.T) {
	nick, sev, reason := sentinel.ParseIncidentLineForTest("INCIDENT | nick:   spacey  | severity:   high   | reason:   trim me   ")
	if nick != "spacey" || sev != "high" || reason != "trim me" {
		t.Errorf("trim failed: nick=%q sev=%q reason=%q", nick, sev, reason)
	}
}

// --- severityMeetsMin -------------------------------------------------------

func newBot(cfg sentinel.Config, llm sentinel.LLMProvider) *sentinel.Bot {
	return sentinel.New(cfg, llm, nil)
}

func TestSeverityMeetsMinDefault(t *testing.T) {
	b := newBot(sentinel.Config{}, nil)
	cases := map[string]bool{
		"low":     false, // default is medium
		"medium":  true,
		"high":    true,
		"unknown": true, // unknown — report it (fail-open)
	}
	for sev, want := range cases {
		if got := sentinel.SeverityMeetsMinForTest(b, sev); got != want {
			t.Errorf("severity=%q: got %v want %v", sev, got, want)
		}
	}
}

func TestSeverityMeetsMinHigh(t *testing.T) {
	b := newBot(sentinel.Config{MinSeverity: "high"}, nil)
	if sentinel.SeverityMeetsMinForTest(b, "low") {
		t.Error("low should not meet high")
	}
	if sentinel.SeverityMeetsMinForTest(b, "medium") {
		t.Error("medium should not meet high")
	}
	if !sentinel.SeverityMeetsMinForTest(b, "high") {
		t.Error("high should meet high")
	}
}

func TestSeverityMeetsMinLowAcceptsAll(t *testing.T) {
	b := newBot(sentinel.Config{MinSeverity: "low"}, nil)
	for _, sev := range []string{"low", "medium", "high"} {
		if !sentinel.SeverityMeetsMinForTest(b, sev) {
			t.Errorf("MinSeverity=low should accept %q", sev)
		}
	}
}

// --- Config.setDefaults -----------------------------------------------------

func TestConfigSetDefaultsAppliesAllUnsetFields(t *testing.T) {
	cfg := sentinel.Config{}
	sentinel.SetDefaultsForTest(&cfg)

	if cfg.Nick != "sentinel" {
		t.Errorf("Nick: %q", cfg.Nick)
	}
	if cfg.WindowSize != 20 {
		t.Errorf("WindowSize: %d", cfg.WindowSize)
	}
	if cfg.WindowAge != 5*time.Minute {
		t.Errorf("WindowAge: %s", cfg.WindowAge)
	}
	if cfg.CooldownPerNick != 10*time.Minute {
		t.Errorf("CooldownPerNick: %s", cfg.CooldownPerNick)
	}
	if cfg.MinSeverity != "medium" {
		t.Errorf("MinSeverity: %q", cfg.MinSeverity)
	}
	if cfg.ModChannel != "#moderation" {
		t.Errorf("ModChannel: %q", cfg.ModChannel)
	}
	if cfg.Policy == "" {
		t.Error("Policy should default to non-empty string")
	}
}

func TestConfigSetDefaultsPreservesProvidedValues(t *testing.T) {
	cfg := sentinel.Config{
		Nick:            "watcher",
		WindowSize:      50,
		WindowAge:       2 * time.Minute,
		CooldownPerNick: 1 * time.Hour,
		MinSeverity:     "high",
		ModChannel:      "#mod",
		Policy:          "custom policy",
	}
	sentinel.SetDefaultsForTest(&cfg)

	if cfg.Nick != "watcher" || cfg.WindowSize != 50 || cfg.WindowAge != 2*time.Minute ||
		cfg.CooldownPerNick != time.Hour || cfg.MinSeverity != "high" ||
		cfg.ModChannel != "#mod" || cfg.Policy != "custom policy" {
		t.Errorf("setDefaults clobbered explicit values: %+v", cfg)
	}
}

// --- buildPrompt ------------------------------------------------------------

func TestBuildPromptIncludesPolicyChannelAndMessages(t *testing.T) {
	b := newBot(sentinel.Config{Policy: "no spam"}, nil)
	now := time.Date(2026, 4, 23, 14, 30, 0, 0, time.UTC)
	msgs := []sentinel.TestMsg{
		{At: now, Nick: "alice", Text: "hello"},
		{At: now.Add(time.Second), Nick: "bob", Text: "spam spam spam"},
	}
	prompt := sentinel.BuildPromptForTest(b, "#general", msgs)

	if !strings.Contains(prompt, "no spam") {
		t.Error("prompt should embed policy")
	}
	if !strings.Contains(prompt, "#general") {
		t.Error("prompt should mention channel")
	}
	if !strings.Contains(prompt, "alice: hello") {
		t.Errorf("prompt missing alice line:\n%s", prompt)
	}
	if !strings.Contains(prompt, "bob: spam spam spam") {
		t.Errorf("prompt missing bob line:\n%s", prompt)
	}
	if !strings.Contains(prompt, "INCIDENT") || !strings.Contains(prompt, "CLEAN") {
		t.Error("prompt must instruct INCIDENT/CLEAN response format")
	}
	if !strings.Contains(prompt, "Messages (2)") {
		t.Errorf("prompt should annotate message count, got:\n%s", prompt)
	}
}

func TestBuildPromptDefaultsPolicyWhenEmpty(t *testing.T) {
	// New() applies defaults, so policy will be the default text — verify it
	// shows up in the prompt rather than an empty placeholder.
	b := newBot(sentinel.Config{}, nil)
	prompt := sentinel.BuildPromptForTest(b, "#x", []sentinel.TestMsg{
		{At: time.Now(), Nick: "n", Text: "hi"},
	})
	if !strings.Contains(prompt, "harassment") || !strings.Contains(prompt, "spam") {
		t.Errorf("default policy should be embedded, got:\n%s", prompt)
	}
}

// --- pruneTimeMap -----------------------------------------------------------

func TestPruneTimeMapDropsExpired(t *testing.T) {
	m := map[string]time.Time{
		"old":   time.Now().Add(-2 * time.Hour),
		"fresh": time.Now().Add(-1 * time.Minute),
	}
	sentinel.PruneTimeMapForTest(m, time.Hour)
	if _, ok := m["old"]; ok {
		t.Error("old entry should be pruned")
	}
	if _, ok := m["fresh"]; !ok {
		t.Error("fresh entry should be retained")
	}
}

func TestPruneTimeMapEmptyIsNoop(t *testing.T) {
	m := map[string]time.Time{}
	sentinel.PruneTimeMapForTest(m, time.Hour)
	if len(m) != 0 {
		t.Error("empty map should remain empty")
	}
}

// --- splitHostPort ----------------------------------------------------------

func TestSplitHostPortValid(t *testing.T) {
	host, port, err := sentinel.SplitHostPortForTest("irc.example.com:6667")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if host != "irc.example.com" || port != 6667 {
		t.Errorf("got %s:%d", host, port)
	}
}

func TestSplitHostPortInvalid(t *testing.T) {
	if _, _, err := sentinel.SplitHostPortForTest("no-port"); err == nil {
		t.Error("expected error on missing port")
	}
	if _, _, err := sentinel.SplitHostPortForTest("host:notanint"); err == nil {
		t.Error("expected error on non-numeric port")
	}
}

// --- LLMProvider interface — verify our fake conforms -----------------------

func TestFakeLLMConformsToProvider(t *testing.T) {
	var _ sentinel.LLMProvider = (*fakeLLM)(nil)

	f := &fakeLLM{resp: "CLEAN"}
	out, err := f.Summarize(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out != "CLEAN" {
		t.Errorf("got %q", out)
	}
	if f.calls() != 1 || f.lastPrompt() != "hi" {
		t.Errorf("calls=%d lastPrompt=%q", f.calls(), f.lastPrompt())
	}
}

func TestFakeLLMReturnsError(t *testing.T) {
	f := &fakeLLM{err: errors.New("boom")}
	if _, err := f.Summarize(context.Background(), "x"); err == nil {
		t.Error("expected error to propagate")
	}
}

// --- New() ------------------------------------------------------------------

func TestNewAppliesDefaults(t *testing.T) {
	b := sentinel.New(sentinel.Config{}, nil, nil)
	if b == nil {
		t.Fatal("nil bot")
	}
	// Verify defaults landed by checking severityMeetsMin behaviour: with
	// MinSeverity defaulting to medium, "low" must not meet.
	if sentinel.SeverityMeetsMinForTest(b, "low") {
		t.Error("New() should default MinSeverity to medium")
	}
}

// --- buffer() ---------------------------------------------------------------

func TestBufferAccumulatesUntilWindowSize(t *testing.T) {
	b := newBot(sentinel.Config{WindowSize: 3}, &fakeLLM{resp: "CLEAN"})
	ctx := context.Background()

	sentinel.BufferForTest(b, ctx, "#x", "alice", "one")
	sentinel.BufferForTest(b, ctx, "#x", "alice", "two")

	if got := sentinel.BufferLen(b, "#x"); got != 2 {
		t.Errorf("buffer len: %d want 2", got)
	}
}

func TestBufferTriggersAnalyseAtWindowSize(t *testing.T) {
	llm := &fakeLLM{resp: "CLEAN"}
	b := newBot(sentinel.Config{WindowSize: 2}, llm)
	ctx := context.Background()

	sentinel.BufferForTest(b, ctx, "#x", "alice", "one")
	sentinel.BufferForTest(b, ctx, "#x", "alice", "two")

	// On window fill, buffer is drained and an analyse goroutine is spawned.
	if got := sentinel.BufferLen(b, "#x"); got != 0 {
		t.Errorf("after window fill, buffer should be drained: len=%d", got)
	}

	// Wait briefly for analyse goroutine to call the LLM.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if llm.calls() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if llm.calls() != 1 {
		t.Fatalf("expected exactly 1 LLM call, got %d", llm.calls())
	}
	if !strings.Contains(llm.lastPrompt(), "alice") {
		t.Error("LLM prompt should contain buffered nick")
	}
}

func TestBufferIsolatesPerChannel(t *testing.T) {
	b := newBot(sentinel.Config{WindowSize: 100}, nil)
	ctx := context.Background()
	sentinel.BufferForTest(b, ctx, "#a", "alice", "1")
	sentinel.BufferForTest(b, ctx, "#a", "alice", "2")
	sentinel.BufferForTest(b, ctx, "#b", "bob", "1")

	if got := sentinel.BufferLen(b, "#a"); got != 2 {
		t.Errorf("#a: %d", got)
	}
	if got := sentinel.BufferLen(b, "#b"); got != 1 {
		t.Errorf("#b: %d", got)
	}
}

// --- flushStale -------------------------------------------------------------

func TestFlushStaleSkipsFreshBuffers(t *testing.T) {
	b := newBot(sentinel.Config{WindowSize: 100, WindowAge: time.Hour}, &fakeLLM{resp: "CLEAN"})
	ctx := context.Background()

	sentinel.PushBuffer(b, "#x", "alice", "msg", time.Now())
	// Buffer was just created (lastScan ~now), so flushStale should not flush it.
	sentinel.FlushStaleForTest(b, ctx)

	if got := sentinel.BufferLen(b, "#x"); got != 1 {
		t.Errorf("buffer should be untouched: len=%d", got)
	}
}

func TestFlushStaleFlushesAgedBuffers(t *testing.T) {
	llm := &fakeLLM{resp: "CLEAN"}
	b := newBot(sentinel.Config{WindowSize: 100, WindowAge: 10 * time.Millisecond}, llm)
	ctx := context.Background()

	sentinel.PushBuffer(b, "#x", "alice", "stale", time.Now())
	sentinel.AgeBuffer(b, "#x", time.Hour) // make it stale

	sentinel.FlushStaleForTest(b, ctx)

	if got := sentinel.BufferLen(b, "#x"); got != 0 {
		t.Errorf("aged buffer should be drained: len=%d", got)
	}

	// Wait for analyse goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if llm.calls() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if llm.calls() != 1 {
		t.Errorf("expected 1 LLM call after flush, got %d", llm.calls())
	}
}

func TestFlushStaleSkipsEmptyBuffers(t *testing.T) {
	b := newBot(sentinel.Config{WindowSize: 100, WindowAge: time.Millisecond}, &fakeLLM{resp: "CLEAN"})
	ctx := context.Background()
	// Create an empty buffer manually by pushing then draining.
	sentinel.PushBuffer(b, "#x", "alice", "msg", time.Now())
	sentinel.AgeBuffer(b, "#x", time.Hour)
	sentinel.FlushStaleForTest(b, ctx) // drains
	// Now buffer exists but empty; flushStale should not re-flush.
	sentinel.FlushStaleForTest(b, ctx)
	// No assertion needed — we're verifying it doesn't panic / spawn goroutines.
	// Wait briefly to allow scheduling so race detector catches issues.
	time.Sleep(20 * time.Millisecond)
}

// --- analyse ---------------------------------------------------------------

func TestAnalyseEarlyReturnOnNoLLM(t *testing.T) {
	b := newBot(sentinel.Config{}, nil)
	// No panic, no LLM. Should be a fast no-op.
	sentinel.AnalyseForTest(b, context.Background(), "#x", []sentinel.TestMsg{
		{At: time.Now(), Nick: "n", Text: "t"},
	})
}

func TestAnalyseEarlyReturnOnEmptyMsgs(t *testing.T) {
	llm := &fakeLLM{resp: "CLEAN"}
	b := newBot(sentinel.Config{}, llm)
	sentinel.AnalyseForTest(b, context.Background(), "#x", nil)
	if llm.calls() != 0 {
		t.Error("empty window should not call LLM")
	}
}

func TestAnalyseCallsLLMAndDoesNotReportOnClean(t *testing.T) {
	llm := &fakeLLM{resp: "CLEAN"}
	b := newBot(sentinel.Config{}, llm)
	sentinel.AnalyseForTest(b, context.Background(), "#x", []sentinel.TestMsg{
		{At: time.Now(), Nick: "alice", Text: "hello"},
	})
	if llm.calls() != 1 {
		t.Fatalf("LLM called %d times", llm.calls())
	}
	// CLEAN should not register cooldown.
	if got := sentinel.CooldownLen(b); got != 0 {
		t.Errorf("cooldown should be empty for CLEAN result, got %d", got)
	}
}

func TestAnalyseSwallowsLLMError(t *testing.T) {
	llm := &fakeLLM{err: errors.New("rate limit")}
	b := newBot(sentinel.Config{}, llm)
	// Must not panic.
	sentinel.AnalyseForTest(b, context.Background(), "#x", []sentinel.TestMsg{
		{At: time.Now(), Nick: "alice", Text: "hi"},
	})
}

func TestAnalyseRespectsCancelledContext(t *testing.T) {
	llm := &fakeLLM{resp: "CLEAN"}
	b := newBot(sentinel.Config{}, llm)

	// Saturate the analyseSlot semaphore with blocked goroutines so the next
	// analyse() call must wait. Then cancel the context and verify it returns.
	// We do this by calling analyse() with a context already cancelled and a
	// non-empty msgs list; the semaphore acquire path is fast normally, but
	// the cancelled-ctx select should still be honoured.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// With cancelled ctx, analyse may either acquire the slot (it's empty) and
	// proceed, or hit the ctx.Done() branch. We just assert no panic.
	sentinel.AnalyseForTest(b, ctx, "#x", []sentinel.TestMsg{
		{At: time.Now(), Nick: "n", Text: "t"},
	})
}

// --- parseAndReport (client-less path) --------------------------------------

func TestParseAndReportNoClientIsNoop(t *testing.T) {
	b := newBot(sentinel.Config{}, nil)
	// No client wired: parseAndReport returns immediately, no cooldown side-effects.
	sentinel.ParseAndReportForTest(b, "#x", "INCIDENT | nick: alice | severity: high | reason: spam")
	if got := sentinel.CooldownLen(b); got != 0 {
		t.Errorf("client-less parseAndReport should not touch cooldown, got len=%d", got)
	}
}
