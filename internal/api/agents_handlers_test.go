package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/conflicthq/scuttlebot/internal/auth"
	"github.com/conflicthq/scuttlebot/internal/bots/bridge"
	"github.com/conflicthq/scuttlebot/internal/registry"
)

// blockerBridge is a minimal chatBridge that just captures Send calls so we can
// assert that handleAgentBlocker forwards an alert to #ops.
type blockerBridge struct {
	mu    sync.Mutex
	sends []struct {
		channel string
		text    string
	}
}

func (b *blockerBridge) Channels() []string               { return nil }
func (b *blockerBridge) JoinChannel(string)               {}
func (b *blockerBridge) LeaveChannel(string)              {}
func (b *blockerBridge) Messages(string) []bridge.Message { return nil }
func (b *blockerBridge) Subscribe(string) (<-chan bridge.Message, func()) {
	return make(chan bridge.Message), func() {}
}
func (b *blockerBridge) Send(_ context.Context, ch, text, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sends = append(b.sends, struct {
		channel string
		text    string
	}{ch, text})
	return nil
}
func (b *blockerBridge) SendWithMeta(_ context.Context, _, _, _ string, _ *bridge.Meta) error {
	return nil
}
func (b *blockerBridge) Stats() bridge.Stats                     { return bridge.Stats{} }
func (b *blockerBridge) TouchUser(string, string)                {}
func (b *blockerBridge) Users(string) []string                   { return nil }
func (b *blockerBridge) UsersWithModes(string) []bridge.UserInfo { return nil }
func (b *blockerBridge) ChannelModes(string) string              { return "" }
func (b *blockerBridge) RefreshNames(string)                     {}
func (b *blockerBridge) SetMode(string, string, string)          {}

// newAgentsTestServer creates a server with a real registry but no topology.
func newAgentsTestServer(t *testing.T) (*httptest.Server, *registry.Registry) {
	t.Helper()
	reg := registry.New(newStubProvisioner(), []byte("key"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(reg, auth.TestStore("tok"), nil, nil, nil, nil, nil, nil, nil, "", false, false, log).Handler())
	t.Cleanup(srv.Close)
	return srv, reg
}

// newAgentsTestServerWithBridge wires a blocker-recording bridge.
func newAgentsTestServerWithBridge(t *testing.T) (*httptest.Server, *registry.Registry, *blockerBridge) {
	t.Helper()
	br := &blockerBridge{}
	reg := registry.New(newStubProvisioner(), []byte("key"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(reg, auth.TestStore("tok"), br, nil, nil, nil, nil, nil, nil, "", false, false, log).Handler())
	t.Cleanup(srv.Close)
	return srv, reg, br
}

func agentsDo(t *testing.T, srv *httptest.Server, method, path string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req, _ := http.NewRequest(method, srv.URL+path, &buf)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

// --- handleAdopt ---

func TestHandleAdoptCreatesAgent(t *testing.T) {
	srv, reg := newAgentsTestServer(t)

	resp := agentsDo(t, srv, http.MethodPost, "/v1/agents/adoptee/adopt", map[string]any{
		"type":     "worker",
		"channels": []string{"#fleet"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["nick"] != "adoptee" {
		t.Errorf("nick = %v, want adoptee", body["nick"])
	}
	if body["payload"] == nil {
		t.Error("expected payload in adopt response")
	}

	a, err := reg.Get("adoptee")
	if err != nil {
		t.Fatalf("get adoptee: %v", err)
	}
	if a.Type != registry.AgentTypeWorker {
		t.Errorf("type = %q, want worker", a.Type)
	}
}

func TestHandleAdoptDuplicate(t *testing.T) {
	srv, _ := newAgentsTestServer(t)

	body := map[string]any{"channels": []string{"#fleet"}}
	first := agentsDo(t, srv, http.MethodPost, "/v1/agents/dup/adopt", body)
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first adopt: want 200, got %d", first.StatusCode)
	}

	second := agentsDo(t, srv, http.MethodPost, "/v1/agents/dup/adopt", body)
	defer second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("second adopt: want 409, got %d", second.StatusCode)
	}
}

func TestHandleAdoptInvalidJSON(t *testing.T) {
	srv, _ := newAgentsTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/agents/x/adopt", bytes.NewReader([]byte("not json")))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// --- handleBulkDeleteAgents ---

func TestHandleBulkDeleteAgents(t *testing.T) {
	srv, reg := newAgentsTestServer(t)

	for _, nick := range []string{"a", "b", "c"} {
		if _, _, err := reg.Register(nick, registry.AgentTypeWorker, registry.EngagementConfig{Channels: []string{"#fleet"}}); err != nil {
			t.Fatalf("register %s: %v", nick, err)
		}
	}

	resp := agentsDo(t, srv, http.MethodPost, "/v1/agents/bulk-delete", map[string]any{
		"nicks": []string{"a", "b", "missing"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var got map[string]int
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["deleted"] != 2 {
		t.Errorf("deleted = %d, want 2", got["deleted"])
	}
	if got["failed"] != 1 {
		t.Errorf("failed = %d, want 1 (for missing nick)", got["failed"])
	}

	// c should still be present.
	if _, err := reg.Get("c"); err != nil {
		t.Errorf("agent c should still exist: %v", err)
	}
	if _, err := reg.Get("a"); err == nil {
		t.Error("agent a should have been deleted")
	}
}

func TestHandleBulkDeleteEmptyList(t *testing.T) {
	srv, _ := newAgentsTestServer(t)

	resp := agentsDo(t, srv, http.MethodPost, "/v1/agents/bulk-delete", map[string]any{
		"nicks": []string{},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for empty nicks list, got %d", resp.StatusCode)
	}
}

func TestHandleBulkDeleteInvalidJSON(t *testing.T) {
	srv, _ := newAgentsTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/agents/bulk-delete", bytes.NewReader([]byte("nope")))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// --- handleUpdateAgent (LLM config fields, channels, team) ---

func TestHandleUpdateAgentLLMConfig(t *testing.T) {
	srv, reg := newAgentsTestServer(t)

	if _, _, err := reg.Register("upd-1", registry.AgentTypeWorker, registry.EngagementConfig{Channels: []string{"#fleet"}}); err != nil {
		t.Fatalf("register: %v", err)
	}

	temp := 0.42
	resp := agentsDo(t, srv, http.MethodPatch, "/v1/agents/upd-1", map[string]any{
		"system_prompt":  "be terse",
		"model":          "gpt-9000",
		"temperature":    temp,
		"tool_allowlist": []string{"shell", "web"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}

	a, err := reg.Get("upd-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if a.Config.SystemPrompt != "be terse" {
		t.Errorf("system_prompt = %q, want 'be terse'", a.Config.SystemPrompt)
	}
	if a.Config.Model != "gpt-9000" {
		t.Errorf("model = %q, want gpt-9000", a.Config.Model)
	}
	if a.Config.Temperature == nil || *a.Config.Temperature != 0.42 {
		t.Errorf("temperature = %v, want 0.42", a.Config.Temperature)
	}
	if len(a.Config.ToolAllowlist) != 2 || a.Config.ToolAllowlist[1] != "web" {
		t.Errorf("tool_allowlist = %v", a.Config.ToolAllowlist)
	}
}

func TestHandleUpdateAgentChannels(t *testing.T) {
	srv, reg := newAgentsTestServer(t)

	if _, _, err := reg.Register("upd-2", registry.AgentTypeWorker, registry.EngagementConfig{Channels: []string{"#fleet"}}); err != nil {
		t.Fatalf("register: %v", err)
	}

	resp := agentsDo(t, srv, http.MethodPatch, "/v1/agents/upd-2", map[string]any{
		"channels": []string{"#fleet", "#new"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}

	a, err := reg.Get("upd-2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(a.Channels) != 2 || a.Channels[1] != "#new" {
		t.Errorf("channels = %v, want [#fleet #new]", a.Channels)
	}
}

func TestHandleUpdateAgentNotFound(t *testing.T) {
	srv, _ := newAgentsTestServer(t)

	resp := agentsDo(t, srv, http.MethodPatch, "/v1/agents/nobody", map[string]any{
		"system_prompt": "x",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestHandleUpdateAgentInvalidJSON(t *testing.T) {
	srv, reg := newAgentsTestServer(t)

	if _, _, err := reg.Register("upd-3", registry.AgentTypeWorker, registry.EngagementConfig{Channels: []string{"#fleet"}}); err != nil {
		t.Fatalf("register: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/v1/agents/upd-3", bytes.NewReader([]byte("not json")))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// --- handleListAgents skill filter ---

func TestHandleListAgentsSkillFilter(t *testing.T) {
	srv, reg := newAgentsTestServer(t)

	if _, _, err := reg.Register("go-1", registry.AgentTypeWorker, registry.EngagementConfig{Channels: []string{"#fleet"}}); err != nil {
		t.Fatalf("register go-1: %v", err)
	}
	if _, _, err := reg.Register("py-1", registry.AgentTypeWorker, registry.EngagementConfig{Channels: []string{"#fleet"}}); err != nil {
		t.Fatalf("register py-1: %v", err)
	}
	g, _ := reg.Get("go-1")
	g.Skills = []string{"go", "irc"}
	_ = reg.Update(g)
	p, _ := reg.Get("py-1")
	p.Skills = []string{"python"}
	_ = reg.Update(p)

	// ?skill=go: only go-1.
	resp := agentsDo(t, srv, http.MethodGet, "/v1/agents?skill=go", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var got map[string][]struct {
		Nick string `json:"nick"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got["agents"]) != 1 || got["agents"][0].Nick != "go-1" {
		t.Errorf("agents = %+v, want [go-1]", got["agents"])
	}

	// ?skill=Go (case-insensitive) — same result.
	resp2 := agentsDo(t, srv, http.MethodGet, "/v1/agents?skill=Go", nil)
	defer resp2.Body.Close()
	var got2 map[string][]struct {
		Nick string `json:"nick"`
	}
	json.NewDecoder(resp2.Body).Decode(&got2)
	if len(got2["agents"]) != 1 {
		t.Errorf("case-insensitive skill filter failed: %+v", got2)
	}
}

// --- handleAgentBlocker ---

func TestHandleAgentBlockerPostsToOps(t *testing.T) {
	srv, reg, br := newAgentsTestServerWithBridge(t)

	if _, _, err := reg.Register("stuck-1", registry.AgentTypeWorker, registry.EngagementConfig{Channels: []string{"#fleet"}}); err != nil {
		t.Fatalf("register: %v", err)
	}

	resp := agentsDo(t, srv, http.MethodPost, "/v1/agents/stuck-1/blocker", map[string]any{
		"channel": "#fleet",
		"message": "can't access store",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}

	br.mu.Lock()
	defer br.mu.Unlock()
	if len(br.sends) != 1 {
		t.Fatalf("send calls = %d, want 1", len(br.sends))
	}
	if br.sends[0].channel != "#ops" {
		t.Errorf("channel = %q, want #ops", br.sends[0].channel)
	}
	if br.sends[0].text == "" {
		t.Error("expected non-empty alert text")
	}
}

func TestHandleAgentBlockerMissingMessage(t *testing.T) {
	srv, reg, _ := newAgentsTestServerWithBridge(t)

	if _, _, err := reg.Register("stuck-2", registry.AgentTypeWorker, registry.EngagementConfig{Channels: []string{"#fleet"}}); err != nil {
		t.Fatalf("register: %v", err)
	}

	resp := agentsDo(t, srv, http.MethodPost, "/v1/agents/stuck-2/blocker", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 (missing message), got %d", resp.StatusCode)
	}
}

func TestHandleAgentBlockerUnknownAgent(t *testing.T) {
	srv, _, _ := newAgentsTestServerWithBridge(t)

	resp := agentsDo(t, srv, http.MethodPost, "/v1/agents/ghost/blocker", map[string]any{
		"message": "stuck",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

// --- ReconcileAgentModes ---

func TestReconcileAgentModes(t *testing.T) {
	stub := &stubTopologyManager{}
	reg := registry.New(newStubProvisioner(), []byte("key"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(reg, auth.TestStore("tok"), nil, nil, nil, nil, stub, nil, nil, "", false, false, log)

	if _, _, err := reg.Register("worker-r", registry.AgentTypeWorker, registry.EngagementConfig{Channels: []string{"#a", "#b"}}); err != nil {
		t.Fatalf("register: %v", err)
	}

	// ReconcileAgentModes should issue revoke + grant for each channel.
	s.ReconcileAgentModes()

	grants := stub.waitForGrants(2, 1_000_000_000)
	if len(grants) < 2 {
		t.Fatalf("want >=2 grants, got %d", len(grants))
	}
	for _, g := range grants {
		if g.Level != "VOICE" {
			t.Errorf("worker should get VOICE, got %q on %s", g.Level, g.Channel)
		}
	}
}

func TestReconcileAgentModesNoTopology(t *testing.T) {
	// Should be a no-op (and not panic) when topology is nil.
	reg := registry.New(newStubProvisioner(), []byte("key"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(reg, auth.TestStore("tok"), nil, nil, nil, nil, nil, nil, nil, "", false, false, log)
	s.ReconcileAgentModes()
}

// --- SetBotManager wiring ---

type stubBotMgr struct{}

func (s *stubBotMgr) NotifyChannelProvisioned(string) {}
func (s *stubBotMgr) NotifyChannelDropped(string)     {}

func TestSetBotManager(t *testing.T) {
	reg := registry.New(newStubProvisioner(), []byte("key"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(reg, auth.TestStore("tok"), nil, nil, nil, nil, nil, nil, nil, "", false, false, log)
	s.SetBotManager(&stubBotMgr{})
	// Idempotent re-set.
	s.SetBotManager(&stubBotMgr{})
}
