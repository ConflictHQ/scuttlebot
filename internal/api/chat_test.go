package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/conflicthq/scuttlebot/internal/bots/bridge"
	"github.com/conflicthq/scuttlebot/internal/registry"
)

type stubChatBridge struct {
	touched []struct {
		channel string
		nick    string
	}
}

func (b *stubChatBridge) Channels() []string               { return nil }
func (b *stubChatBridge) JoinChannel(string)               {}
func (b *stubChatBridge) LeaveChannel(string)              {}
func (b *stubChatBridge) Messages(string) []bridge.Message { return nil }
func (b *stubChatBridge) Subscribe(string) (<-chan bridge.Message, func()) {
	return make(chan bridge.Message), func() {}
}
func (b *stubChatBridge) Send(context.Context, string, string, string) error { return nil }
func (b *stubChatBridge) Stats() bridge.Stats                                { return bridge.Stats{} }
func (b *stubChatBridge) Users(string) []string                              { return nil }
func (b *stubChatBridge) TouchUser(channel, nick string) {
	b.touched = append(b.touched, struct{ channel, nick string }{channel: channel, nick: nick})
}

func TestHandleChannelPresence(t *testing.T) {
	t.Helper()

	bridgeStub := &stubChatBridge{}
	reg := registry.New(nil, []byte("test-signing-key"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(reg, []string{"token"}, bridgeStub, nil, nil, nil, nil, nil, "", logger).Handler())
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{"nick": "codex-test"})
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/channels/general/presence", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	if len(bridgeStub.touched) != 1 {
		t.Fatalf("TouchUser calls = %d, want 1", len(bridgeStub.touched))
	}
	if bridgeStub.touched[0].channel != "#general" || bridgeStub.touched[0].nick != "codex-test" {
		t.Fatalf("TouchUser args = %#v", bridgeStub.touched[0])
	}
}

func TestHandleChannelPresenceRequiresNick(t *testing.T) {
	t.Helper()

	bridgeStub := &stubChatBridge{}
	reg := registry.New(nil, []byte("test-signing-key"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(reg, []string{"token"}, bridgeStub, nil, nil, nil, nil, nil, "", logger).Handler())
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{})
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/channels/general/presence", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	if len(bridgeStub.touched) != 0 {
		t.Fatalf("TouchUser calls = %d, want 0", len(bridgeStub.touched))
	}
}
