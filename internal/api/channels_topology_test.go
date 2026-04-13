package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/internal/auth"
	"github.com/conflicthq/scuttlebot/internal/config"
	"github.com/conflicthq/scuttlebot/internal/registry"
	"github.com/conflicthq/scuttlebot/internal/topology"
)

// accessCall records a single GrantAccess or RevokeAccess invocation.
type accessCall struct {
	Nick    string
	Channel string
	Level   string // "OP", "VOICE", or "" for revoke
}

// stubTopologyManager implements topologyManager for tests.
// It records the last ProvisionChannel call and returns a canned Policy.
type stubTopologyManager struct {
	last    topology.ChannelConfig
	policy  *topology.Policy
	provErr error
	grants  []accessCall
	revokes []accessCall
}

func (s *stubTopologyManager) ProvisionChannel(ch topology.ChannelConfig) error {
	s.last = ch
	return s.provErr
}

func (s *stubTopologyManager) DropChannel(_ string) {}

func (s *stubTopologyManager) Policy() *topology.Policy { return s.policy }

func (s *stubTopologyManager) GrantAccess(nick, channel, level string) {
	s.grants = append(s.grants, accessCall{Nick: nick, Channel: channel, Level: level})
}

func (s *stubTopologyManager) RevokeAccess(nick, channel string) {
	s.revokes = append(s.revokes, accessCall{Nick: nick, Channel: channel})
}

func (s *stubTopologyManager) ListChannels() []topology.ChannelInfo { return nil }

// stubProvisioner is a minimal AccountProvisioner for agent registration tests.
type stubProvisioner struct {
	accounts map[string]string
}

func newStubProvisioner() *stubProvisioner {
	return &stubProvisioner{accounts: make(map[string]string)}
}

func (p *stubProvisioner) RegisterAccount(name, pass string) error {
	if _, ok := p.accounts[name]; ok {
		return fmt.Errorf("ACCOUNT_EXISTS")
	}
	p.accounts[name] = pass
	return nil
}

func (p *stubProvisioner) ChangePassword(name, pass string) error {
	p.accounts[name] = pass
	return nil
}

func newTopoTestServer(t *testing.T, topo *stubTopologyManager) (*httptest.Server, string) {
	t.Helper()
	reg := registry.New(nil, []byte("key"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(reg, auth.TestStore("tok"), nil, nil, nil, nil, topo, nil, nil, "", log).Handler())
	t.Cleanup(srv.Close)
	return srv, "tok"
}

// newTopoTestServerWithRegistry creates a test server with both topology and a
// real registry backed by stubProvisioner, so agent registration works.
func newTopoTestServerWithRegistry(t *testing.T, topo *stubTopologyManager) (*httptest.Server, string) {
	t.Helper()
	reg := registry.New(newStubProvisioner(), []byte("key"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(reg, auth.TestStore("tok"), nil, nil, nil, nil, topo, nil, nil, "", log).Handler())
	t.Cleanup(srv.Close)
	return srv, "tok"
}

func TestHandleProvisionChannel(t *testing.T) {
	pol := topology.NewPolicy(config.TopologyConfig{
		Types: []config.ChannelTypeConfig{
			{
				Name:     "task",
				Prefix:   "task.",
				Autojoin: []string{"bridge", "scribe"},
				TTL:      config.Duration{Duration: 72 * time.Hour},
			},
		},
	})
	stub := &stubTopologyManager{policy: pol}
	srv, tok := newTopoTestServer(t, stub)

	body, _ := json.Marshal(map[string]string{"name": "#task.gh-1"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/channels", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d", resp.StatusCode)
	}
	var got struct {
		Channel  string   `json:"channel"`
		Type     string   `json:"type"`
		Autojoin []string `json:"autojoin"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Channel != "#task.gh-1" {
		t.Errorf("channel = %q, want #task.gh-1", got.Channel)
	}
	if got.Type != "task" {
		t.Errorf("type = %q, want task", got.Type)
	}
	if len(got.Autojoin) != 2 || got.Autojoin[0] != "bridge" {
		t.Errorf("autojoin = %v, want [bridge scribe]", got.Autojoin)
	}
	// Verify autojoin was forwarded to ProvisionChannel.
	if len(stub.last.Autojoin) != 2 {
		t.Errorf("stub.last.Autojoin = %v, want [bridge scribe]", stub.last.Autojoin)
	}
}

func TestHandleProvisionChannelInvalidName(t *testing.T) {
	stub := &stubTopologyManager{}
	srv, tok := newTopoTestServer(t, stub)

	body, _ := json.Marshal(map[string]string{"name": "no-hash"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/channels", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400, got %d", resp.StatusCode)
	}
}

func TestHandleGetTopology(t *testing.T) {
	pol := topology.NewPolicy(config.TopologyConfig{
		Channels: []config.StaticChannelConfig{{Name: "#general"}},
		Types: []config.ChannelTypeConfig{
			{Name: "task", Prefix: "task.", Autojoin: []string{"bridge"}},
		},
	})
	stub := &stubTopologyManager{policy: pol}
	srv, tok := newTopoTestServer(t, stub)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/topology", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var got struct {
		StaticChannels []string `json:"static_channels"`
		Types          []struct {
			Name   string `json:"name"`
			Prefix string `json:"prefix"`
		} `json:"types"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.StaticChannels) != 1 || got.StaticChannels[0] != "#general" {
		t.Errorf("static_channels = %v, want [#general]", got.StaticChannels)
	}
	if len(got.Types) != 1 || got.Types[0].Name != "task" {
		t.Errorf("types = %v", got.Types)
	}
}

// --- Agent mode assignment tests ---

// topoDoJSON is a helper for issuing authenticated JSON requests against a test server.
func topoDoJSON(t *testing.T, srv *httptest.Server, tok, method, path string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req, _ := http.NewRequest(method, srv.URL+path, &buf)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func TestRegisterGrantsOPForOrchestrator(t *testing.T) {
	stub := &stubTopologyManager{}
	srv, tok := newTopoTestServerWithRegistry(t, stub)

	resp := topoDoJSON(t, srv, tok, "POST", "/v1/agents/register", map[string]any{
		"nick":     "orch-1",
		"type":     "orchestrator",
		"channels": []string{"#fleet", "#project.foo"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", resp.StatusCode)
	}

	if len(stub.grants) != 2 {
		t.Fatalf("grants: want 2, got %d", len(stub.grants))
	}
	for i, want := range []accessCall{
		{Nick: "orch-1", Channel: "#fleet", Level: "OP"},
		{Nick: "orch-1", Channel: "#project.foo", Level: "OP"},
	} {
		if stub.grants[i] != want {
			t.Errorf("grant[%d] = %+v, want %+v", i, stub.grants[i], want)
		}
	}
}

func TestRegisterGrantsVOICEForWorker(t *testing.T) {
	stub := &stubTopologyManager{}
	srv, tok := newTopoTestServerWithRegistry(t, stub)

	resp := topoDoJSON(t, srv, tok, "POST", "/v1/agents/register", map[string]any{
		"nick":     "worker-1",
		"type":     "worker",
		"channels": []string{"#fleet"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", resp.StatusCode)
	}

	if len(stub.grants) != 1 {
		t.Fatalf("grants: want 1, got %d", len(stub.grants))
	}
	if stub.grants[0].Level != "VOICE" {
		t.Errorf("level = %q, want VOICE", stub.grants[0].Level)
	}
}

func TestRegisterNoModeForObserver(t *testing.T) {
	stub := &stubTopologyManager{}
	srv, tok := newTopoTestServerWithRegistry(t, stub)

	resp := topoDoJSON(t, srv, tok, "POST", "/v1/agents/register", map[string]any{
		"nick":     "obs-1",
		"type":     "observer",
		"channels": []string{"#fleet"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", resp.StatusCode)
	}

	if len(stub.grants) != 0 {
		t.Errorf("grants: want 0, got %d — observer should get no mode", len(stub.grants))
	}
}

func TestRegisterGrantsOPForOperator(t *testing.T) {
	stub := &stubTopologyManager{}
	srv, tok := newTopoTestServerWithRegistry(t, stub)

	resp := topoDoJSON(t, srv, tok, "POST", "/v1/agents/register", map[string]any{
		"nick":     "human-op",
		"type":     "operator",
		"channels": []string{"#fleet"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", resp.StatusCode)
	}

	if len(stub.grants) != 1 {
		t.Fatalf("grants: want 1, got %d", len(stub.grants))
	}
	if stub.grants[0].Level != "OP" {
		t.Errorf("level = %q, want OP", stub.grants[0].Level)
	}
}

func TestRegisterOrchestratorWithOpsChannels(t *testing.T) {
	stub := &stubTopologyManager{}
	srv, tok := newTopoTestServerWithRegistry(t, stub)

	resp := topoDoJSON(t, srv, tok, "POST", "/v1/agents/register", map[string]any{
		"nick":         "orch-ops",
		"type":         "orchestrator",
		"channels":     []string{"#fleet", "#project.foo", "#project.bar"},
		"ops_channels": []string{"#fleet"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", resp.StatusCode)
	}

	if len(stub.grants) != 3 {
		t.Fatalf("grants: want 3, got %d", len(stub.grants))
	}
	for i, want := range []accessCall{
		{Nick: "orch-ops", Channel: "#fleet", Level: "OP"},
		{Nick: "orch-ops", Channel: "#project.foo", Level: "VOICE"},
		{Nick: "orch-ops", Channel: "#project.bar", Level: "VOICE"},
	} {
		if stub.grants[i] != want {
			t.Errorf("grant[%d] = %+v, want %+v", i, stub.grants[i], want)
		}
	}
}

func TestRevokeRemovesAccess(t *testing.T) {
	stub := &stubTopologyManager{}
	srv, tok := newTopoTestServerWithRegistry(t, stub)

	resp := topoDoJSON(t, srv, tok, "POST", "/v1/agents/register", map[string]any{
		"nick":     "orch-rev",
		"type":     "orchestrator",
		"channels": []string{"#fleet", "#project.x"},
	})
	resp.Body.Close()

	resp = topoDoJSON(t, srv, tok, "POST", "/v1/agents/orch-rev/revoke", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: want 204, got %d", resp.StatusCode)
	}

	if len(stub.revokes) != 2 {
		t.Fatalf("revokes: want 2, got %d", len(stub.revokes))
	}
	for i, want := range []string{"#fleet", "#project.x"} {
		if stub.revokes[i].Channel != want {
			t.Errorf("revoke[%d].Channel = %q, want %q", i, stub.revokes[i].Channel, want)
		}
	}
}

func TestDeleteRemovesAccess(t *testing.T) {
	stub := &stubTopologyManager{}
	srv, tok := newTopoTestServerWithRegistry(t, stub)

	resp := topoDoJSON(t, srv, tok, "POST", "/v1/agents/register", map[string]any{
		"nick":     "del-agent",
		"type":     "worker",
		"channels": []string{"#fleet"},
	})
	resp.Body.Close()

	resp = topoDoJSON(t, srv, tok, "DELETE", "/v1/agents/del-agent", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d", resp.StatusCode)
	}

	if len(stub.revokes) != 1 {
		t.Fatalf("revokes: want 1, got %d", len(stub.revokes))
	}
	if stub.revokes[0].Nick != "del-agent" || stub.revokes[0].Channel != "#fleet" {
		t.Errorf("revoke = %+v, want del-agent on #fleet", stub.revokes[0])
	}
}
