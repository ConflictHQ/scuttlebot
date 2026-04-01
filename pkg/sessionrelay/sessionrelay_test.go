package sessionrelay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPConnectorPostMessagesAndTouch(t *testing.T) {
	t.Helper()

	base := time.Date(2026, 3, 31, 22, 0, 0, 0, time.UTC)
	var gotAuth string
	var posted map[string]string
	var touched map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/channels/general/messages":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatalf("decode post body: %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/channels/general/presence":
			if err := json.NewDecoder(r.Body).Decode(&touched); err != nil {
				t.Fatalf("decode touch body: %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/channels/general/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]string{
				{"at": base.Add(-time.Second).Format(time.RFC3339Nano), "nick": "old", "text": "ignore"},
				{"at": base.Add(time.Second).Format(time.RFC3339Nano), "nick": "glengoolie", "text": "codex-test: check README"},
			}})
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
		Nick:       "codex-test",
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := conn.Post(context.Background(), "online"); err != nil {
		t.Fatal(err)
	}
	if posted["nick"] != "codex-test" || posted["text"] != "online" {
		t.Fatalf("posted body = %#v", posted)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("authorization = %q", gotAuth)
	}

	msgs, err := conn.MessagesSince(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Nick != "glengoolie" {
		t.Fatalf("MessagesSince = %#v", msgs)
	}

	if err := conn.Touch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if touched["nick"] != "codex-test" {
		t.Fatalf("touch body = %#v", touched)
	}
}

func TestHTTPConnectorTouchIgnoresMissingPresenceEndpoint(t *testing.T) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/channels/general/presence" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	conn, err := New(Config{
		Transport:  TransportHTTP,
		URL:        srv.URL,
		Token:      "test-token",
		Channel:    "general",
		Nick:       "codex-test",
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := conn.Touch(context.Background()); err != nil {
		t.Fatalf("Touch() = %v, want nil on 404", err)
	}
}

func TestIRCRegisterOrRotateCreatesAndDeletes(t *testing.T) {
	t.Helper()

	var deletedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/register":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"credentials": map[string]string{"passphrase": "created-pass"},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/agents/codex-1234":
			deletedPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	conn := &ircConnector{
		http:          srv.Client(),
		apiURL:        srv.URL,
		token:         "test-token",
		nick:          "codex-1234",
		channel:       "#general",
		agentType:     "worker",
		deleteOnClose: true,
	}

	created, pass, err := conn.registerOrRotate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !created || pass != "created-pass" {
		t.Fatalf("registerOrRotate = (%v, %q), want (true, created-pass)", created, pass)
	}
	conn.registeredByRelay = created
	if err := conn.cleanupRegistration(context.Background()); err != nil {
		t.Fatal(err)
	}
	if deletedPath != "/v1/agents/codex-1234" {
		t.Fatalf("delete path = %q", deletedPath)
	}
}

func TestIRCRegisterOrRotateFallsBackToRotate(t *testing.T) {
	t.Helper()

	var rotateCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/register":
			w.WriteHeader(http.StatusConflict)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/codex-1234/rotate":
			rotateCalled = true
			_ = json.NewEncoder(w).Encode(map[string]string{"passphrase": "rotated-pass"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	conn := &ircConnector{
		http:      srv.Client(),
		apiURL:    srv.URL,
		token:     "test-token",
		nick:      "codex-1234",
		channel:   "#general",
		agentType: "worker",
	}

	created, pass, err := conn.registerOrRotate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("created = true, want false when register conflicts")
	}
	if !rotateCalled || pass != "rotated-pass" {
		t.Fatalf("rotate fallback = (called=%v, pass=%q)", rotateCalled, pass)
	}
}
