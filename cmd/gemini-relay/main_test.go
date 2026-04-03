package main

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/pkg/sessionrelay"
)

func TestFilterMessages(t *testing.T) {
	now := time.Now()
	nick := "gemini-test"
	messages := []message{
		{Nick: "operator", Text: "gemini-test: hello", At: now},
		{Nick: "gemini-test", Text: "i am gemini", At: now}, // self
		{Nick: "other", Text: "not for me", At: now},        // no mention
		{Nick: "bridge", Text: "system message", At: now},   // service bot
	}

	filtered, _ := filterMessages(messages, now.Add(-time.Minute), nick, "worker")
	if len(filtered) != 1 {
		t.Errorf("expected 1 filtered message, got %d", len(filtered))
	}
	if filtered[0].Nick != "operator" {
		t.Errorf("expected operator message, got %s", filtered[0].Nick)
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("SCUTTLEBOT_CONFIG_FILE", filepath.Join(t.TempDir(), "scuttlebot-relay.env"))
	t.Setenv("SCUTTLEBOT_URL", "http://test:8080")
	t.Setenv("SCUTTLEBOT_TOKEN", "test-token")
	t.Setenv("GEMINI_SESSION_ID", "abc")
	t.Setenv("SCUTTLEBOT_TRANSPORT", "irc")
	t.Setenv("SCUTTLEBOT_IRC_ADDR", "127.0.0.1:7667")
	t.Setenv("SCUTTLEBOT_SESSION_ID", "")
	t.Setenv("SCUTTLEBOT_NICK", "")

	cfg, err := loadConfig([]string{"--cd", "../.."})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.URL != "http://test:8080" {
		t.Errorf("expected URL http://test:8080, got %s", cfg.URL)
	}
	if cfg.Token != "test-token" {
		t.Errorf("expected token test-token, got %s", cfg.Token)
	}
	if cfg.SessionID != "abc" {
		t.Errorf("expected session ID abc, got %s", cfg.SessionID)
	}
	if cfg.Nick != "gemini-scuttlebot-abc" {
		t.Errorf("expected nick gemini-scuttlebot-abc, got %s", cfg.Nick)
	}
	if cfg.Transport != sessionrelay.TransportIRC {
		t.Errorf("expected transport irc, got %s", cfg.Transport)
	}
	if cfg.IRCAddr != "127.0.0.1:7667" {
		t.Errorf("expected irc addr 127.0.0.1:7667, got %s", cfg.IRCAddr)
	}
}

func TestRelayStateShouldInterruptOnlyWhenRecentlyBusy(t *testing.T) {
	t.Helper()

	var state relayState
	now := time.Date(2026, 3, 31, 21, 47, 0, 0, time.UTC)
	state.observeOutput([]byte("Working (1s • esc to interrupt)"), now)

	if !state.shouldInterrupt(now.Add(defaultBusyWindow / 2)) {
		t.Fatal("shouldInterrupt = false, want true for recent busy session")
	}
	if state.shouldInterrupt(now.Add(defaultBusyWindow + time.Millisecond)) {
		t.Fatal("shouldInterrupt = true, want false after busy window expires")
	}
}

func TestInjectMessagesIdleSkipsCtrlCAndSubmits(t *testing.T) {
	t.Helper()

	var writer bytes.Buffer
	cfg := config{
		Nick:               "gemini-scuttlebot-1234",
		InterruptOnMessage: true,
	}
	state := &relayState{}
	batch := []message{{
		Nick: "glengoolie",
		Text: "gemini-scuttlebot-1234: check README.md",
	}}

	if err := injectMessages(&writer, cfg, state, "#general", batch); err != nil {
		t.Fatal(err)
	}

	want := bracketedPasteStart + "[IRC operator messages]\n[general] glengoolie: check README.md\n" + bracketedPasteEnd + "\r"
	if writer.String() != want {
		t.Fatalf("injectMessages idle = %q, want %q", writer.String(), want)
	}
}

func TestInjectMessagesBusySendsCtrlCBeforeSubmit(t *testing.T) {
	t.Helper()

	var writer bytes.Buffer
	cfg := config{
		Nick:               "gemini-scuttlebot-1234",
		InterruptOnMessage: true,
	}
	state := &relayState{}
	state.observeOutput([]byte("Working (2s • esc to interrupt)"), time.Now())
	batch := []message{{
		Nick: "glengoolie",
		Text: "gemini-scuttlebot-1234: stop and re-read bridge.go",
	}}

	if err := injectMessages(&writer, cfg, state, "#general", batch); err != nil {
		t.Fatal(err)
	}

	want := string([]byte{3}) + bracketedPasteStart + "[IRC operator messages]\n[general] glengoolie: stop and re-read bridge.go\n" + bracketedPasteEnd + "\r"
	if writer.String() != want {
		t.Fatalf("injectMessages busy = %q, want %q", writer.String(), want)
	}
}
