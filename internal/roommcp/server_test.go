package roommcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/internal/bots/bridge"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testBackend struct {
	messages []bridge.Message
	deny     bool
	sends    int
}

func (b *testBackend) Call(_ context.Context, method, path string, body, result any) error {
	if b.deny {
		return &APIError{Status: 403}
	}
	if method == "GET" && strings.HasSuffix(path, "/messages") && result != nil {
		data, _ := json.Marshal(map[string]any{"messages": b.messages})
		return json.Unmarshal(data, result)
	}
	if method == "POST" && strings.HasSuffix(path, "/messages") {
		b.sends++
		m := body.(map[string]string)
		b.messages = append(b.messages, bridge.Message{At: time.Unix(100, int64(b.sends)), Channel: "#room", Nick: m["nick"], Text: m["text"]})
	}
	return nil
}
func testService(t *testing.T, path string, b *testBackend, owner string) *Service {
	t.Helper()
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Service{Store: store, Resolve: func(*mcp.CallToolRequest) (Access, error) { return Access{Owner: owner, Backend: b}, nil }}
}
func TestReplayUntilAcknowledgedAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rooms.sqlite")
	b := &testBackend{}
	s := testService(t, path, b, "owner")
	in := SessionInput{Session: "codex-project", Channel: "room"}
	_, opened, err := s.open(ctx, nil, in)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.send(ctx, nil, SendInput{SessionInput: in, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	_, first, err := s.read(ctx, nil, ReadInput{SessionInput: in})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Messages) != 1 || first.Messages[0].Nick != opened.Nick {
		t.Fatalf("unexpected messages: %+v", first)
	}
	// Server restarts and the source buffer is now empty. The durable journal
	// still returns the unacknowledged message with the same cursor and identity.
	if err = s.Store.Close(); err != nil {
		t.Fatal(err)
	}
	b.messages = nil
	s = testService(t, path, b, "owner")
	_, reopened, err := s.open(ctx, nil, in)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Nick != opened.Nick {
		t.Fatal("identity changed")
	}
	_, again, err := s.read(ctx, nil, ReadInput{SessionInput: in})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Messages) != 1 || again.NextCursor != first.NextCursor {
		t.Fatalf("replay lost: %+v", again)
	}
	if _, _, err = s.ack(ctx, nil, AckInput{SessionInput: in, Cursor: again.NextCursor}); err != nil {
		t.Fatal(err)
	}
	_, empty, err := s.read(ctx, nil, ReadInput{SessionInput: in})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Messages) != 0 {
		t.Fatal("acknowledged message replayed")
	}
}
func TestAuthorizationAndCursorBounds(t *testing.T) {
	ctx := context.Background()
	b := &testBackend{}
	s := testService(t, filepath.Join(t.TempDir(), "rooms.sqlite"), b, "alice")
	in := SessionInput{Session: "coding", Channel: "room"}
	if _, _, err := s.open(ctx, nil, in); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := s.send(ctx, nil, SendInput{SessionInput: in, Text: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	_, batch, err := s.read(ctx, nil, ReadInput{SessionInput: in, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Messages) != 1 || !batch.More {
		t.Fatalf("pagination: %+v", batch)
	}
	if _, _, err = s.ack(ctx, nil, AckInput{SessionInput: in, Cursor: "9999"}); err == nil {
		t.Fatal("accepted unoffered cursor")
	}
	if _, _, err = s.send(ctx, nil, SendInput{SessionInput: in, Text: "bad\r\nPRIVMSG #other :inject"}); err == nil {
		t.Fatal("accepted multiline injection")
	}
	if _, _, err = s.send(ctx, nil, SendInput{SessionInput: SessionInput{Session: in.Session, Channel: "elsewhere"}, Text: "hello"}); err == nil {
		t.Fatal("sent without membership")
	}
	s.Resolve = func(*mcp.CallToolRequest) (Access, error) { return Access{Owner: "bob", Backend: b}, nil }
	if _, _, err = s.read(ctx, nil, ReadInput{SessionInput: in}); err == nil {
		t.Fatal("read another principal's session")
	}
	s.Resolve = func(*mcp.CallToolRequest) (Access, error) { return Access{Owner: "alice", Backend: b}, nil }
	b.deny = true
	if _, _, err = s.read(ctx, nil, ReadInput{SessionInput: in}); err == nil {
		t.Fatal("cached history bypassed revoked access")
	}
}
func TestMCPHandshakeAndToolCalls(t *testing.T) {
	s := testService(t, filepath.Join(t.TempDir(), "rooms.sqlite"), &testBackend{}, "owner")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, b := mcp.NewInMemoryTransports()
	server, err := s.Server().Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 6 {
		t.Fatalf("tools=%d", len(tools.Tools))
	}
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "open_session", Arguments: map[string]any{"session": "test", "channel": "room"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool failed: %+v", result)
	}
}

func TestStateIsolationAndConcurrentUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rooms.sqlite")
	a := testService(t, path, &testBackend{}, "owner")
	b := testService(t, path, &testBackend{}, "owner")
	if err := a.Store.BindEndpoint("https://one.example"); err != nil {
		t.Fatal(err)
	}
	if err := b.Store.BindEndpoint("https://two.example"); err == nil {
		t.Fatal("mixed server journals")
	}
	if _, _, err := a.open(context.Background(), nil, SessionInput{Session: "shared", Channel: "room"}); err != nil {
		t.Fatal(err)
	}
	x, err := a.Store.load("owner", "shared")
	if err != nil {
		t.Fatal(err)
	}
	y, err := b.Store.load("owner", "shared")
	if err != nil {
		t.Fatal(err)
	}
	x.Cursors["#room"] = 5
	if err := a.Store.save("owner", x); err != nil {
		t.Fatal(err)
	}
	y.Cursors["#room"] = 3
	if err := b.Store.save("owner", y); err == nil {
		t.Fatal("stale writer rewound saved cursor")
	}
}

func TestJournalOnlyJoinedRoomsAndDeduplicates(t *testing.T) {
	s := testService(t, filepath.Join(t.TempDir(), "rooms.sqlite"), &testBackend{}, "owner")
	m := bridge.Message{Channel: "#room", At: time.Unix(100, 0), Nick: "peer", Text: "hello"}
	if err := s.Store.Record(m); err != nil {
		t.Fatal(err)
	}
	msgs, _, err := s.Store.messages("#room", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatal("journaled an unjoined room")
	}
	if _, _, err := s.open(context.Background(), nil, SessionInput{Session: "reader", Channel: "room"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.Store.Record(m); err != nil {
			t.Fatal(err)
		}
	}
	msgs, _, err = s.Store.messages("#room", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("deduplication: %d messages", len(msgs))
	}
}
