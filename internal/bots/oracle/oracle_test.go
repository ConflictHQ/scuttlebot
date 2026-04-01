package oracle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/conflicthq/scuttlebot/internal/bots/oracle"
)

// --- mock history ---

type mockHistory struct {
	entries map[string][]oracle.HistoryEntry
}

func (m *mockHistory) Query(channel string, limit int) ([]oracle.HistoryEntry, error) {
	entries := m.entries[channel]
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func newHistory(channel string, entries []oracle.HistoryEntry) *mockHistory {
	return &mockHistory{entries: map[string][]oracle.HistoryEntry{channel: entries}}
}

// --- ParseCommand tests ---

func TestParseCommandValid(t *testing.T) {
	tests := []struct {
		input   string
		channel string
		limit   int
		format  oracle.Format
	}{
		{"summarize #fleet", "#fleet", 50, oracle.FormatTOON},
		{"summarize #fleet last=20", "#fleet", 20, oracle.FormatTOON},
		{"summarize #fleet last=100 format=json", "#fleet", 100, oracle.FormatJSON},
		{"summarize #project.test format=toon last=10", "#project.test", 10, oracle.FormatTOON},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			req, err := oracle.ParseCommand(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if req.Channel != tt.channel {
				t.Errorf("Channel: got %q, want %q", req.Channel, tt.channel)
			}
			if req.Limit != tt.limit {
				t.Errorf("Limit: got %d, want %d", req.Limit, tt.limit)
			}
			if req.Format != tt.format {
				t.Errorf("Format: got %q, want %q", req.Format, tt.format)
			}
		})
	}
}

func TestParseCommandInvalid(t *testing.T) {
	tests := []struct {
		input string
	}{
		{"summarize"},                        // missing channel
		{""},                                 // empty
		{"do-something #fleet"},              // unknown command
		{"summarize fleet"},                  // missing #
		{"summarize #fleet last=notanumber"}, // bad last
		{"summarize #fleet format=xml"},      // unknown format
		{"summarize #fleet last=-5"},         // negative
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if _, err := oracle.ParseCommand(tt.input); err == nil {
				t.Errorf("expected error for %q, got nil", tt.input)
			}
		})
	}
}

func TestParseCommandLimitCap(t *testing.T) {
	req, err := oracle.ParseCommand("summarize #fleet last=9999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Limit > 200 {
		t.Errorf("limit should be capped at 200, got %d", req.Limit)
	}
}

// --- Bot construction ---

func TestBotName(t *testing.T) {
	b := oracle.New("localhost:6667", "pass",
		newHistory("#fleet", nil),
		&oracle.StubProvider{Response: "summary"},
		nil,
	)
	if b.Name() != "oracle" {
		t.Errorf("Name(): got %q", b.Name())
	}
}

// --- StubProvider ---

func TestStubProviderReturnsResponse(t *testing.T) {
	p := &oracle.StubProvider{Response: "the fleet is idle"}
	summary, err := p.Summarize(context.TODO(), "prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary != "the fleet is idle" {
		t.Errorf("got %q", summary)
	}
}

func TestStubProviderReturnsError(t *testing.T) {
	p := &oracle.StubProvider{Err: errors.New("llm unavailable")}
	_, err := p.Summarize(context.TODO(), "prompt")
	if err == nil {
		t.Error("expected error")
	}
}

// --- HistoryFetcher ---

func TestHistoryFetcherReturnsEntries(t *testing.T) {
	h := newHistory("#fleet", []oracle.HistoryEntry{
		{Nick: "agent-01", MessageType: "task.create", Raw: `{"v":1}`},
		{Nick: "human", Raw: "looks good"},
	})
	entries, err := h.Query("#fleet", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}
}

func TestHistoryFetcherEmptyChannel(t *testing.T) {
	h := newHistory("#fleet", nil)
	entries, err := h.Query("#empty", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestHistoryFetcherLimitRespected(t *testing.T) {
	entries := make([]oracle.HistoryEntry, 100)
	for i := range entries {
		entries[i] = oracle.HistoryEntry{Nick: "a", Raw: "msg"}
	}
	h := newHistory("#fleet", entries)
	got, _ := h.Query("#fleet", 10)
	if len(got) != 10 {
		t.Errorf("expected 10 entries (limit), got %d", len(got))
	}
}
