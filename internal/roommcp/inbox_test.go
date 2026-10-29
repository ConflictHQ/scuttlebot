package roommcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type inboxFake struct {
	mu       sync.Mutex
	messages []Message
	ack      int
	replies  []string
}

func (f *inboxFake) Open(context.Context, SessionInput) (OpenResult, error) {
	return OpenResult{Session: "worker", Nick: "mcp-worker-123", Channel: "#room"}, nil
}
func (f *inboxFake) Read(context.Context, SessionInput) (ReadResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := ReadResult{Messages: []Message{}}
	for _, m := range f.messages {
		n, _ := strconv.Atoi(m.Cursor)
		if n > f.ack {
			out.Messages = append(out.Messages, m)
			out.NextCursor = m.Cursor
		}
	}
	return out, nil
}
func (f *inboxFake) Ack(_ context.Context, _ SessionInput, c string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ack, _ = strconv.Atoi(c)
	return nil
}
func (f *inboxFake) Send(_ context.Context, _ SessionInput, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, text)
	return nil
}

func TestExplicitRecipientRouting(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"@worker: help", true}, {"@WORKER help", true}, {"@other @worker: coordinate", true},
		{"@other, @worker coordinate", true}, {"ambient chatter", false}, {"we mentioned @worker yesterday", false},
		{"> @worker: quoted", false}, {"@worker-extra: help", false}, {"@other: ask @worker", false},
	} {
		if got := addressedTo(tc.text, "worker"); got != tc.want {
			t.Errorf("%q: got %v want %v", tc.text, got, tc.want)
		}
	}
}

func TestFleetHandles(t *testing.T) {
	for _, tc := range []struct {
		text, session string
		want          bool
	}{
		{"@agents: hello", "codex-build", true}, {"@agents hello", "claude-review", true},
		{"@codex: check", "codex-build", true}, {"@codex: check", "claude-review", false},
		{"@claude: review", "claude-review", true}, {"@calliope: check", "calliope-build", true},
		{"@calliope: check", "codex-build", false}, {"@claude @codex: coordinate", "codex-build", true},
		{"we discussed @agents", "codex-build", false}, {"@codex-extra: hi", "codex-build", false},
	} {
		if got := addressedToAgent(tc.text, tc.session, "mcp-"+tc.session+"-123"); got != tc.want {
			t.Errorf("%q to %s: got %v want %v", tc.text, tc.session, got, tc.want)
		}
	}
}
func TestInboxIgnoresNoiseAndWaitsForAcknowledgment(t *testing.T) {
	f := &inboxFake{messages: []Message{
		{Cursor: "1", Nick: "peer", Text: "chatter"}, {Cursor: "2", Nick: "stranger", Text: "@worker: untrusted"},
		{Cursor: "3", Nick: "peer", Text: "@worker: real request"}, {Cursor: "4", Nick: "mcp-worker-123", Text: "@worker self"},
		{Cursor: "5", Nick: "peer", Text: "discussion of @worker"},
	}}
	w := &Inbox{Remote: f, Input: SessionInput{Session: "worker", Channel: "room"}, AllowedSenders: []string{"peer"}, opened: OpenResult{Nick: "mcp-worker-123", Channel: "#room"}}
	var events []Event
	emit := func(_ context.Context, e Event) error { events = append(events, e); return nil }
	ctx := context.Background()
	if err := w.poll(ctx, emit, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Meta["cursor"] != "3" {
		t.Fatalf("wrong events: %+v", events)
	}
	if f.ack != 2 {
		t.Fatalf("acked unprocessed target: %d", f.ack)
	}
	if err := w.poll(ctx, emit, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatal("duplicate notification")
	}
	if err := f.Ack(ctx, w.Input, "3"); err != nil {
		t.Fatal(err)
	}
	if err := w.poll(ctx, emit, time.Now()); err != nil {
		t.Fatal(err)
	}
	if f.ack != 5 {
		t.Fatal("did not advance ignored tail")
	}
}
func TestInboxLimitsOutstandingWakeups(t *testing.T) {
	f := &inboxFake{}
	for i := 1; i <= 10; i++ {
		f.messages = append(f.messages, Message{Cursor: strconv.Itoa(i), Nick: "peer", Text: "@worker: work"})
	}
	w := &Inbox{Remote: f, Input: SessionInput{Session: "worker"}, AllowedSenders: []string{"peer"}, opened: OpenResult{Nick: "self"}}
	n := 0
	emit := func(context.Context, Event) error { n++; return nil }
	now := time.Now()
	if err := w.poll(context.Background(), emit, now); err != nil {
		t.Fatal(err)
	}
	if err := w.poll(context.Background(), emit, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n != 6 || f.ack != 0 {
		t.Fatalf("unbounded wakeups or auto-ack: n=%d ack=%d", n, f.ack)
	}
}
func TestClaudeChannelProtocolAndNotification(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f := &inboxFake{messages: []Message{{Cursor: "1", Nick: "peer", Text: "@worker: hello"}}}
	w := &Inbox{Remote: f, Input: SessionInput{Session: "worker", Channel: "room"}, AllowedSenders: []string{"peer"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	defer inWriter.Close()
	defer outReader.Close()
	done := make(chan error, 1)
	go func() { done <- RunClaudeChannel(ctx, w, &mcp.IOTransport{Reader: inReader, Writer: outWriter}) }()
	enc := json.NewEncoder(inWriter)
	dec := json.NewDecoder(bufio.NewReader(outReader))
	if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "test", "version": "1"}}}); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			Capabilities    struct {
				Experimental map[string]any `json:"experimental"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	if err := dec.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Result.ProtocolVersion != "2025-11-25" {
		t.Fatal("unsupported channel protocol")
	}
	if _, ok := response.Result.Capabilities.Experimental["claude/channel"]; !ok {
		t.Fatal("missing channel capability")
	}
	if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		t.Fatal(err)
	}
	var notification struct {
		Method string `json:"method"`
		Params Event  `json:"params"`
	}
	for notification.Method == "" {
		if err := dec.Decode(&notification); err != nil {
			t.Fatal(err)
		}
	}
	if notification.Method != "notifications/claude/channel" || notification.Params.Content != "@worker: hello" {
		t.Fatalf("wrong event: %+v", notification)
	}
	if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "mark_read", "arguments": map[string]string{"cursor": "1"}}}); err != nil {
		t.Fatal(err)
	}
	var ack map[string]any
	if err := dec.Decode(&ack); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	n := f.ack
	f.mu.Unlock()
	if n != 1 {
		t.Fatal("mark_read did not acknowledge")
	}
	_ = inWriter.Close()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("channel did not stop")
	}
}
