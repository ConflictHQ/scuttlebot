package sessionrelay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"
)

func TestHTTPConnectorPostMessagesAndTouch(t *testing.T) {
	t.Helper()

	base := time.Date(2026, 3, 31, 22, 0, 0, 0, time.UTC)
	var gotAuth []string
	var posted []string
	var touched []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/channels/general/messages":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode general post body: %v", err)
			}
			posted = append(posted, "general:"+body["nick"]+":"+body["text"])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/channels/release/messages":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode release post body: %v", err)
			}
			posted = append(posted, "release:"+body["nick"]+":"+body["text"])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/channels/general/presence":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode general touch body: %v", err)
			}
			touched = append(touched, "general:"+body["nick"])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/channels/release/presence":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode release touch body: %v", err)
			}
			touched = append(touched, "release:"+body["nick"])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/channels/general/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]string{
				{"at": base.Add(-time.Second).Format(time.RFC3339Nano), "nick": "old", "text": "ignore"},
				{"at": base.Add(time.Second).Format(time.RFC3339Nano), "nick": "glengoolie", "text": "codex-test: check README"},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/channels/release/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]string{
				{"at": base.Add(2 * time.Second).Format(time.RFC3339Nano), "nick": "glengoolie", "text": "codex-test: /join #task-42"},
			}})
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
		Channels:   []string{"general", "release"},
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
	if want := []string{"general:codex-test:online", "release:codex-test:online"}; !slices.Equal(posted, want) {
		t.Fatalf("posted = %#v, want %#v", posted, want)
	}
	for _, auth := range gotAuth {
		if auth != "Bearer test-token" {
			t.Fatalf("authorization = %q", auth)
		}
	}

	msgs, err := conn.MessagesSince(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("MessagesSince len = %d, want 2", len(msgs))
	}
	if msgs[0].Channel != "#general" || msgs[1].Channel != "#release" {
		t.Fatalf("MessagesSince channels = %#v", msgs)
	}

	if err := conn.Touch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"general:codex-test", "release:codex-test"}; !slices.Equal(touched, want) {
		t.Fatalf("touches = %#v, want %#v", touched, want)
	}
}

func TestHTTPConnectorJoinPartAndControlChannel(t *testing.T) {
	t.Helper()

	conn, err := New(Config{
		Transport: TransportHTTP,
		URL:       "http://example.com",
		Token:     "test-token",
		Channel:   "general",
		Channels:  []string{"general", "release"},
		Nick:      "codex-test",
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := conn.ControlChannel(); got != "#general" {
		t.Fatalf("ControlChannel = %q, want #general", got)
	}
	if err := conn.JoinChannel(context.Background(), "#task-42"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"#general", "#release", "#task-42"}; !slices.Equal(conn.Channels(), want) {
		t.Fatalf("Channels after join = %#v, want %#v", conn.Channels(), want)
	}
	if err := conn.PartChannel(context.Background(), "#general"); err == nil {
		t.Fatal("PartChannel(control) = nil, want error")
	}
	if err := conn.PartChannel(context.Background(), "#release"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"#general", "#task-42"}; !slices.Equal(conn.Channels(), want) {
		t.Fatalf("Channels after part = %#v, want %#v", conn.Channels(), want)
	}
}

func TestIRCRegisterOrRotateCreatesAndDeletes(t *testing.T) {
	t.Helper()

	var deletedPath string
	var registerChannels []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/register":
			var body struct {
				Channels []string `json:"channels"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode register body: %v", err)
			}
			registerChannels = body.Channels
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
		primary:       "#general",
		channels:      []string{"#general", "#release"},
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
	if want := []string{"#general", "#release"}; !slices.Equal(registerChannels, want) {
		t.Fatalf("register channels = %#v, want %#v", registerChannels, want)
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
		primary:   "#general",
		channels:  []string{"#general"},
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

func TestWriteChannelStateFile(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	path := dir + "/channels.env"
	if err := WriteChannelStateFile(path, "general", []string{"#general", "#release"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "SCUTTLEBOT_CHANNEL=general\nSCUTTLEBOT_CHANNELS=general,release\n"
	if string(data) != want {
		t.Fatalf("state file = %q, want %q", string(data), want)
	}
}

func TestParseBrokerCommand(t *testing.T) {
	t.Helper()

	tests := []struct {
		input string
		want  BrokerCommand
		ok    bool
	}{
		{input: "/channels", want: BrokerCommand{Name: "channels"}, ok: true},
		{input: "/join task-42", want: BrokerCommand{Name: "join", Channel: "#task-42"}, ok: true},
		{input: "/part #release", want: BrokerCommand{Name: "part", Channel: "#release"}, ok: true},
		{input: "please read README", ok: false},
	}

	for _, tt := range tests {
		got, ok := ParseBrokerCommand(tt.input)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("ParseBrokerCommand(%q) = (%#v, %v), want (%#v, %v)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}
