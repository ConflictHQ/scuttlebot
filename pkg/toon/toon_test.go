package toon

import (
	"strings"
	"testing"
	"time"
)

func TestFormatEmpty(t *testing.T) {
	if got := Format(nil, Options{}); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestFormatBasic(t *testing.T) {
	base := time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC)
	entries := []Entry{
		{Nick: "alice", Type: "op", Text: "let's ship it", At: base},
		{Nick: "claude-abc", Type: "orch", MessageType: "task.create", Text: "editing main.go", At: base.Add(2 * time.Minute)},
		{Nick: "claude-abc", Type: "orch", MessageType: "task.complete", Text: "done", At: base.Add(5 * time.Minute)},
	}
	out := Format(entries, Options{Channel: "#fleet"})

	// Header.
	if !strings.HasPrefix(out, "#fleet 3msg") {
		t.Errorf("header mismatch: %q", out)
	}
	// Grouped consecutive messages from claude-abc.
	if strings.Count(out, "claude-abc") != 1 {
		t.Errorf("expected nick grouping, got:\n%s", out)
	}
	// Contains message types.
	if !strings.Contains(out, "task.create") || !strings.Contains(out, "task.complete") {
		t.Errorf("missing message types:\n%s", out)
	}
}

func TestFormatPrompt(t *testing.T) {
	entries := []Entry{{Nick: "a", Text: "hello"}}
	out := FormatPrompt("#test", entries)
	if !strings.Contains(out, "Summarize") {
		t.Errorf("prompt missing instruction:\n%s", out)
	}
	if !strings.Contains(out, "#test") {
		t.Errorf("prompt missing channel:\n%s", out)
	}
}

func TestCompactDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{2 * time.Hour, "2h"},
		{2*time.Hour + 30*time.Minute, "2h30m"},
		{48 * time.Hour, "2d"},
	}
	for _, tt := range tests {
		if got := compactDuration(tt.d); got != tt.want {
			t.Errorf("compactDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
