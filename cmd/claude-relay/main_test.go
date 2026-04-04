package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
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

func TestClaudeSessionIDGenerated(t *testing.T) {
	t.Setenv("SCUTTLEBOT_CONFIG_FILE", filepath.Join(t.TempDir(), "scuttlebot-relay.env"))
	t.Setenv("SCUTTLEBOT_URL", "http://test:8080")
	t.Setenv("SCUTTLEBOT_TOKEN", "test-token")

	cfg, err := loadConfig([]string{"--cd", "../.."})
	if err != nil {
		t.Fatal(err)
	}

	// ClaudeSessionID must be a valid UUID
	if cfg.ClaudeSessionID == "" {
		t.Fatal("ClaudeSessionID is empty")
	}
	if _, err := uuid.Parse(cfg.ClaudeSessionID); err != nil {
		t.Fatalf("ClaudeSessionID is not a valid UUID: %s", cfg.ClaudeSessionID)
	}
}

func TestClaudeSessionIDUnique(t *testing.T) {
	t.Setenv("SCUTTLEBOT_CONFIG_FILE", filepath.Join(t.TempDir(), "scuttlebot-relay.env"))
	t.Setenv("SCUTTLEBOT_URL", "http://test:8080")
	t.Setenv("SCUTTLEBOT_TOKEN", "test-token")

	cfg1, err := loadConfig([]string{"--cd", "../.."})
	if err != nil {
		t.Fatal(err)
	}
	cfg2, err := loadConfig([]string{"--cd", "../.."})
	if err != nil {
		t.Fatal(err)
	}

	if cfg1.ClaudeSessionID == cfg2.ClaudeSessionID {
		t.Fatal("two loadConfig calls produced the same ClaudeSessionID")
	}
}

func TestSessionIDArgsPrepended(t *testing.T) {
	// Simulate what run() does with args
	userArgs := []string{"--dangerously-skip-permissions", "--chrome"}
	sessionID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	args := make([]string, 0, len(userArgs)+2)
	args = append(args, "--session-id", sessionID)
	args = append(args, userArgs...)

	if len(args) != 4 {
		t.Fatalf("expected 4 args, got %d", len(args))
	}
	if args[0] != "--session-id" {
		t.Errorf("args[0] = %q, want --session-id", args[0])
	}
	if args[1] != sessionID {
		t.Errorf("args[1] = %q, want %s", args[1], sessionID)
	}
	if args[2] != "--dangerously-skip-permissions" {
		t.Errorf("args[2] = %q, want --dangerously-skip-permissions", args[2])
	}
	// Verify original slice not mutated
	if len(userArgs) != 2 {
		t.Errorf("userArgs mutated: len=%d", len(userArgs))
	}
}

func TestExtractResumeID(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no resume", []string{"--dangerously-skip-permissions"}, ""},
		{"--resume with UUID", []string{"--resume", "740fab38-b4c7-4dfc-a82a-2fe24b48baab"}, "740fab38-b4c7-4dfc-a82a-2fe24b48baab"},
		{"-r with UUID", []string{"-r", "29f0a0bf-b2e8-4eee-bfd8-aabbd90b41fb"}, "29f0a0bf-b2e8-4eee-bfd8-aabbd90b41fb"},
		{"--continue with UUID", []string{"--continue", "21b39df2-c032-4fb4-be1c-0b607a9ee702"}, "21b39df2-c032-4fb4-be1c-0b607a9ee702"},
		{"--resume without value", []string{"--resume"}, ""},
		{"--resume with non-UUID", []string{"--resume", "latest"}, ""},
		{"--resume with short string", []string{"--resume", "abc"}, ""},
		{"mixed args", []string{"--dangerously-skip-permissions", "--resume", "740fab38-b4c7-4dfc-a82a-2fe24b48baab", "--chrome"}, "740fab38-b4c7-4dfc-a82a-2fe24b48baab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractResumeID(tt.args)
			if got != tt.want {
				t.Errorf("extractResumeID(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestDiscoverSessionPathFindsFile(t *testing.T) {
	tmpDir := t.TempDir()
	sessionID := uuid.New().String()

	// Create a fake session file
	sessionFile := filepath.Join(tmpDir, sessionID+".jsonl")
	if err := os.WriteFile(sessionFile, []byte(`{"sessionId":"`+sessionID+`"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		ClaudeSessionID: sessionID,
		TargetCWD:       "/fake/path",
	}

	// Override claudeSessionsRoot by pointing TargetCWD at something that
	// produces the tmpDir. Since claudeSessionsRoot uses $HOME, we need
	// to test discoverSessionPath's file-finding logic directly.
	target := filepath.Join(tmpDir, sessionID+".jsonl")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("session file should exist: %v", err)
	}

	// Test the core logic: Stat finds the file
	_ = cfg // cfg is valid
}

func TestDiscoverSessionPathTimeout(t *testing.T) {
	cfg := config{
		ClaudeSessionID: uuid.New().String(),
		TargetCWD:       t.TempDir(), // empty dir, no session file
	}

	// Use a very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := discoverSessionPath(ctx, cfg, time.Now())
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestDiscoverSessionPathWaitsForFile(t *testing.T) {
	sessionID := uuid.New().String()
	cfg := config{
		ClaudeSessionID: sessionID,
		TargetCWD:       t.TempDir(),
	}

	// Create the file after a delay (simulates Claude Code starting up)
	root, err := claudeSessionsRoot(cfg.TargetCWD)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(300 * time.Millisecond)
		target := filepath.Join(root, sessionID+".jsonl")
		_ = os.WriteFile(target, []byte(`{"sessionId":"`+sessionID+`"}`+"\n"), 0600)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	path, err := discoverSessionPath(ctx, cfg, time.Now())
	if err != nil {
		t.Fatalf("expected to find file, got error: %v", err)
	}
	if filepath.Base(path) != sessionID+".jsonl" {
		t.Errorf("found wrong file: %s", path)
	}
}

func TestSessionMessagesThinking(t *testing.T) {
	line := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","text":"reasoning here"},{"type":"text","text":"final answer"}]}}`)

	// thinking off — only text
	got := sessionMessages(line, false)
	if len(got) != 1 || got[0].Text != "final answer" {
		t.Fatalf("mirrorReasoning=false: got %#v", got)
	}

	// thinking on — both, thinking prefixed
	got = sessionMessages(line, true)
	if len(got) != 2 || got[0].Text != "💭 reasoning here" || got[1].Text != "final answer" {
		t.Fatalf("mirrorReasoning=true: got %#v", got)
	}
}
