package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestFilterMessages(t *testing.T) {
	now := time.Now()
	nick := "claude-test"
	messages := []message{
		{Nick: "operator", Text: "claude-test: hello", At: now},
		{Nick: "claude-test", Text: "i am claude", At: now}, // self
		{Nick: "other", Text: "not for me", At: now},        // no mention
		{Nick: "bridge", Text: "system message", At: now},   // service bot
	}

	filtered, _ := filterMessages(messages, now.Add(-time.Minute), nick)
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
	t.Setenv("SCUTTLEBOT_SESSION_ID", "abc")
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
	if cfg.Nick != "claude-scuttlebot-abc" {
		t.Errorf("expected nick claude-scuttlebot-abc, got %s", cfg.Nick)
	}
}

func TestSessionMessagesThinking(t *testing.T) {
	line := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","text":"reasoning here"},{"type":"text","text":"final answer"}]}}`)

	// thinking off — only text
	got := sessionMessages(line, false)
	if len(got) != 1 || got[0] != "final answer" {
		t.Fatalf("mirrorReasoning=false: got %#v", got)
	}

	// thinking on — both, thinking prefixed
	got = sessionMessages(line, true)
	if len(got) != 2 || got[0] != "💭 reasoning here" || got[1] != "final answer" {
		t.Fatalf("mirrorReasoning=true: got %#v", got)
	}
}
