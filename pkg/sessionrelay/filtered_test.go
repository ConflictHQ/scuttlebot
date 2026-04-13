package sessionrelay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestFilteredConnectorPostAtLevel(t *testing.T) {
	// Track which channels receive which messages.
	var posted []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/channels/general/messages":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode general post: %v", err)
			}
			posted = append(posted, "general:"+body["text"])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/channels/session-abc/messages":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode session post: %v", err)
			}
			posted = append(posted, "session-abc:"+body["text"])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/channels/project-foo/messages":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode project post: %v", err)
			}
			posted = append(posted, "project-foo:"+body["text"])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/register":
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	conn, err := New(Config{
		Transport:  TransportHTTP,
		URL:        srv.URL,
		Token:      "test-token",
		Channel:    "general",
		Channels:   []string{"general", "session-abc", "project-foo"},
		Nick:       "test-relay",
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}

	resMap := map[string]Resolution{
		"#general":     ResFull,    // heartbeat + lifecycle + action + content
		"#session-abc": ResDebug,   // everything including reasoning
		"#project-foo": ResActions, // heartbeat + lifecycle + action only
	}
	filtered := NewFilteredConnector(conn, resMap, ResFull)

	ctx := context.Background()

	// Test 1: LevelContent should go to general and session-abc, NOT project-foo.
	posted = nil
	if err := filtered.PostAtLevel(ctx, LevelContent, "hello world", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"general:hello world", "session-abc:hello world"}
	if !slices.Equal(posted, want) {
		t.Fatalf("LevelContent: posted = %v, want %v", posted, want)
	}

	// Test 2: LevelReasoning should go only to session-abc (ResDebug).
	posted = nil
	if err := filtered.PostAtLevel(ctx, LevelReasoning, "thinking...", nil); err != nil {
		t.Fatal(err)
	}
	want = []string{"session-abc:thinking..."}
	if !slices.Equal(posted, want) {
		t.Fatalf("LevelReasoning: posted = %v, want %v", posted, want)
	}

	// Test 3: LevelAction should go to all three channels.
	posted = nil
	if err := filtered.PostAtLevel(ctx, LevelAction, "running tool", nil); err != nil {
		t.Fatal(err)
	}
	want = []string{"general:running tool", "session-abc:running tool", "project-foo:running tool"}
	if !slices.Equal(posted, want) {
		t.Fatalf("LevelAction: posted = %v, want %v", posted, want)
	}

	// Test 4: LevelLifecycle should go to all three channels.
	posted = nil
	if err := filtered.PostAtLevel(ctx, LevelLifecycle, "online", nil); err != nil {
		t.Fatal(err)
	}
	want = []string{"general:online", "session-abc:online", "project-foo:online"}
	if !slices.Equal(posted, want) {
		t.Fatalf("LevelLifecycle: posted = %v, want %v", posted, want)
	}

	// Test 5: LevelHeartbeat should go to all three channels.
	posted = nil
	if err := filtered.PostAtLevel(ctx, LevelHeartbeat, "heartbeat", nil); err != nil {
		t.Fatal(err)
	}
	want = []string{"general:heartbeat", "session-abc:heartbeat", "project-foo:heartbeat"}
	if !slices.Equal(posted, want) {
		t.Fatalf("LevelHeartbeat: posted = %v, want %v", posted, want)
	}
}

func TestFilteredConnectorPostAtLevelWithMeta(t *testing.T) {
	var posted []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/channels/general/messages":
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			var text string
			_ = json.Unmarshal(body["text"], &text)
			hasMeta := len(body["meta"]) > 0
			posted = append(posted, "general:"+text+":meta="+boolStr(hasMeta))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/register":
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	conn, err := New(Config{
		Transport:  TransportHTTP,
		URL:        srv.URL,
		Token:      "test-token",
		Channel:    "general",
		Channels:   []string{"general"},
		Nick:       "test-relay",
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}

	filtered := NewFilteredConnector(conn, map[string]Resolution{
		"#general": ResFull,
	}, ResFull)

	meta := json.RawMessage(`{"type":"tool_result","data":{"tool":"Bash"}}`)
	if err := filtered.PostAtLevel(context.Background(), LevelAction, "running bash", meta); err != nil {
		t.Fatal(err)
	}
	want := []string{"general:running bash:meta=true"}
	if !slices.Equal(posted, want) {
		t.Fatalf("PostAtLevel with meta: posted = %v, want %v", posted, want)
	}
}

func TestFilteredConnectorSetResolution(t *testing.T) {
	var posted []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/channels/general/messages":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			posted = append(posted, body["text"])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/register":
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	conn, err := New(Config{
		Transport:  TransportHTTP,
		URL:        srv.URL,
		Token:      "test-token",
		Channel:    "general",
		Channels:   []string{"general"},
		Nick:       "test-relay",
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Start with minimal — should filter out content.
	filtered := NewFilteredConnector(conn, map[string]Resolution{
		"#general": ResMinimal,
	}, ResFull)

	ctx := context.Background()
	_ = filtered.PostAtLevel(ctx, LevelContent, "should not appear", nil)
	if len(posted) != 0 {
		t.Fatalf("ResMinimal should filter LevelContent, got %v", posted)
	}

	// Upgrade to full at runtime — now content should pass.
	filtered.SetResolution("#general", ResFull)
	_ = filtered.PostAtLevel(ctx, LevelContent, "should appear", nil)
	if len(posted) != 1 || posted[0] != "should appear" {
		t.Fatalf("After SetResolution to ResFull, posted = %v", posted)
	}
}

func TestFilteredConnectorDefaultResolution(t *testing.T) {
	var posted []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/channels/unknown/messages":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			posted = append(posted, body["text"])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/register":
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	conn, err := New(Config{
		Transport:  TransportHTTP,
		URL:        srv.URL,
		Token:      "test-token",
		Channel:    "unknown",
		Channels:   []string{"unknown"},
		Nick:       "test-relay",
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}

	// No explicit resolution for #unknown — should use default (ResActions).
	filtered := NewFilteredConnector(conn, nil, ResActions)

	ctx := context.Background()

	// LevelAction should pass with default ResActions.
	_ = filtered.PostAtLevel(ctx, LevelAction, "tool call", nil)
	if len(posted) != 1 || posted[0] != "tool call" {
		t.Fatalf("default ResActions should pass LevelAction, got %v", posted)
	}

	// LevelContent should be filtered with default ResActions.
	posted = nil
	_ = filtered.PostAtLevel(ctx, LevelContent, "should be filtered", nil)
	if len(posted) != 0 {
		t.Fatalf("default ResActions should filter LevelContent, got %v", posted)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
