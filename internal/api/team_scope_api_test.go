package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/conflicthq/scuttlebot/internal/api"
	"github.com/conflicthq/scuttlebot/internal/auth"
	"github.com/conflicthq/scuttlebot/internal/bots/bridge"
	"github.com/conflicthq/scuttlebot/internal/registry"
)

const teamToken = "team-token-alpha"

type teamScopeBridge struct {
	joinCalls []string
	sendCalls []string
}

func (b *teamScopeBridge) Channels() []string { return nil }

func (b *teamScopeBridge) JoinChannel(channel string) {
	b.joinCalls = append(b.joinCalls, channel)
}

func (b *teamScopeBridge) LeaveChannel(string) {}

func (b *teamScopeBridge) Messages(string) []bridge.Message {
	return nil
}

func (b *teamScopeBridge) Subscribe(string) (<-chan bridge.Message, func()) {
	return make(chan bridge.Message), func() {}
}

func (b *teamScopeBridge) Send(context.Context, string, string, string) error { return nil }

func (b *teamScopeBridge) SendWithMeta(_ context.Context, channel, text, _ string, _ *bridge.Meta) error {
	b.sendCalls = append(b.sendCalls, channel+":"+text)
	return nil
}

func (b *teamScopeBridge) Stats() bridge.Stats { return bridge.Stats{} }

func (b *teamScopeBridge) TouchUser(string, string) {}

func (b *teamScopeBridge) Users(string) []string { return nil }

func (b *teamScopeBridge) UsersWithModes(string) []bridge.UserInfo { return nil }

func (b *teamScopeBridge) ChannelModes(string) string { return "" }

func (b *teamScopeBridge) RefreshNames(string) {}

func (b *teamScopeBridge) SetMode(string, string, string) {}

func newTeamScopedServer(t *testing.T, bridgeStub *teamScopeBridge) (*httptest.Server, *registry.Registry) {
	t.Helper()
	reg := registry.New(newMock(), []byte("test-signing-key"))
	keys := auth.TestStoreWithTeam("admin-token", teamToken, []auth.Scope{
		auth.ScopeAgents,
		auth.ScopeChannels,
		auth.ScopeChat,
		auth.ScopeTopology,
	}, "alpha")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var srv *api.Server
	if bridgeStub == nil {
		srv = api.New(reg, keys, nil, nil, nil, nil, nil, nil, nil, "", false, false, log)
	} else {
		srv = api.New(reg, keys, bridgeStub, nil, nil, nil, nil, nil, nil, "", false, false, log)
	}
	return httptest.NewServer(srv.Handler()), reg
}

func registerAgentWithTeam(t *testing.T, reg *registry.Registry, nick, team string, channels []string) {
	t.Helper()
	if _, _, err := reg.Register(nick, registry.AgentTypeWorker, registry.EngagementConfig{Channels: channels}); err != nil {
		t.Fatalf("register %s: %v", nick, err)
	}
	agent, err := reg.Get(nick)
	if err != nil {
		t.Fatalf("get %s: %v", nick, err)
	}
	agent.Team = team
	if err := reg.Update(agent); err != nil {
		t.Fatalf("update %s team: %v", nick, err)
	}
}

func TestTeamScopedKeyCannotAccessOtherTeamAgent(t *testing.T) {
	srv, reg := newTeamScopedServer(t, nil)
	defer srv.Close()

	registerAgentWithTeam(t, reg, "alpha-worker", "alpha", []string{"#team-alpha-room"})
	registerAgentWithTeam(t, reg, "bravo-worker", "bravo", []string{"#team-bravo-room"})

	h := http.Header{}
	h.Set("Authorization", "Bearer "+teamToken)

	resp := do(t, srv, http.MethodGet, "/v1/agents/alpha-worker", nil, h)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alpha get: want 200, got %d", resp.StatusCode)
	}

	resp = do(t, srv, http.MethodGet, "/v1/agents/bravo-worker", nil, h)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bravo get: want 404, got %d", resp.StatusCode)
	}
}

func TestTeamScopedKeyCannotAccessOtherTeamChannel(t *testing.T) {
	bridgeStub := &teamScopeBridge{}
	srv, _ := newTeamScopedServer(t, bridgeStub)
	defer srv.Close()

	h := http.Header{}
	h.Set("Authorization", "Bearer "+teamToken)

	resp := do(t, srv, http.MethodGet, "/v1/channels/team-bravo-room/messages", nil, h)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bravo channel get: want 404, got %d", resp.StatusCode)
	}
	if len(bridgeStub.joinCalls) != 0 {
		t.Fatalf("forbidden channel should not auto-join, got %v", bridgeStub.joinCalls)
	}

	resp = do(t, srv, http.MethodPost, "/v1/channels/team-alpha-room/messages", map[string]string{"text": "ok"}, h)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("alpha channel post: want 204, got %d", resp.StatusCode)
	}
	if len(bridgeStub.sendCalls) != 1 {
		t.Fatalf("allowed channel send calls = %d, want 1", len(bridgeStub.sendCalls))
	}
}

func TestTeamScopedRegisterAssignsTeamToPayload(t *testing.T) {
	srv, reg := newTeamScopedServer(t, nil)
	defer srv.Close()

	body := map[string]any{
		"nick":     "alpha-agent",
		"channels": []string{"#team-alpha-room"},
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+teamToken)

	resp := do(t, srv, http.MethodPost, "/v1/agents/register", body, h)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", resp.StatusCode)
	}

	var got struct {
		Payload struct {
			Payload struct {
				Team string `json:"team"`
			} `json:"payload"`
		} `json:"payload"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Payload.Payload.Team != "alpha" {
		t.Fatalf("payload.team = %q, want alpha", got.Payload.Payload.Team)
	}

	agent, err := reg.Get("alpha-agent")
	if err != nil {
		t.Fatalf("get alpha-agent: %v", err)
	}
	if agent.Team != "alpha" {
		t.Fatalf("stored team = %q, want alpha", agent.Team)
	}
}

func TestTeamScopedRegisterRejectsDifferentTeam(t *testing.T) {
	srv, _ := newTeamScopedServer(t, nil)
	defer srv.Close()

	body := map[string]any{
		"nick":     "bad-agent",
		"team":     "bravo",
		"channels": []string{"#team-alpha-room"},
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+teamToken)

	resp := do(t, srv, http.MethodPost, "/v1/agents/register", body, h)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("register: want 403, got %d", resp.StatusCode)
	}
}

func TestTeamScopedStreamRejectsOtherTeamChannel(t *testing.T) {
	bridgeStub := &teamScopeBridge{}
	srv, _ := newTeamScopedServer(t, bridgeStub)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/channels/team-bravo-room/stream?token="+teamToken, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stream: want 404, got %d", resp.StatusCode)
	}
	if len(bridgeStub.joinCalls) != 0 {
		t.Fatalf("forbidden stream should not auto-join, got %v", bridgeStub.joinCalls)
	}
}

func TestTeamScopedKeyCannotRetargetAgentTeam(t *testing.T) {
	srv, reg := newTeamScopedServer(t, nil)
	defer srv.Close()

	registerAgentWithTeam(t, reg, "alpha-worker", "alpha", []string{"#team-alpha-room"})

	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(map[string]string{"team": "bravo"}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	req, err := http.NewRequest(http.MethodPatch, srv.URL+"/v1/agents/alpha-worker", &body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+teamToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("patch team: want 403, got %d", resp.StatusCode)
	}

	agent, err := reg.Get("alpha-worker")
	if err != nil {
		t.Fatalf("get alpha-worker: %v", err)
	}
	if agent.Team != "alpha" {
		t.Fatalf("stored team = %q, want alpha", agent.Team)
	}
}
