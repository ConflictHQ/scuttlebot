package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/internal/auth"
	"github.com/conflicthq/scuttlebot/internal/registry"
)

// newPoliciesTestServer wires a Server with a real PolicyStore (file-backed,
// temp dir) plus a real registry so handleGetRelayConfig can read agent state.
func newPoliciesTestServer(t *testing.T) (*httptest.Server, *PolicyStore, *registry.Registry) {
	t.Helper()
	ps, err := NewPolicyStore(filepath.Join(t.TempDir(), "policies.json"), 5)
	if err != nil {
		t.Fatalf("NewPolicyStore: %v", err)
	}
	reg := registry.New(newStubProvisioner(), []byte("key"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(reg, auth.TestStore("tok"), nil, ps, nil, nil, nil, nil, nil, "", false, false, log).Handler())
	t.Cleanup(srv.Close)
	return srv, ps, reg
}

func policiesDoJSON(t *testing.T, srv *httptest.Server, method, path string, body any) *http.Response {
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

func TestHandleGetPoliciesReturnsDefaults(t *testing.T) {
	srv, _, _ := newPoliciesTestServer(t)

	resp := policiesDoJSON(t, srv, http.MethodGet, "/v1/settings/policies", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var got Policies
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Bridge.WebUserTTLMinutes != 5 {
		t.Errorf("bridge.web_user_ttl_minutes = %d, want 5 (default)", got.Bridge.WebUserTTLMinutes)
	}
	if len(got.Behaviors) == 0 {
		t.Error("expected at least one default behavior")
	}
	if got.OnJoinDefault == "" {
		t.Error("expected non-empty default on-join template")
	}
}

func TestHandlePutPoliciesReplacesAll(t *testing.T) {
	srv, ps, _ := newPoliciesTestServer(t)

	patch := Policies{
		Bridge: BridgePolicy{WebUserTTLMinutes: 17},
		AgentPolicy: AgentPolicy{
			RequireCheckin: true,
			CheckinChannel: "#fleet",
		},
		ChannelResolutions: map[string]string{"#dev": "actions"},
		OnJoinDefault:      "welcome {nick}",
	}
	resp := policiesDoJSON(t, srv, http.MethodPut, "/v1/settings/policies", patch)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	got := ps.Get()
	if got.Bridge.WebUserTTLMinutes != 17 {
		t.Errorf("ttl = %d, want 17", got.Bridge.WebUserTTLMinutes)
	}
	if !got.AgentPolicy.RequireCheckin {
		t.Error("require_checkin should be true")
	}
	if got.OnJoinDefault != "welcome {nick}" {
		t.Errorf("on_join_default = %q, want welcome {nick}", got.OnJoinDefault)
	}
	if got.ChannelResolutions["#dev"] != "actions" {
		t.Errorf("channel_resolutions[#dev] = %q, want actions", got.ChannelResolutions["#dev"])
	}
}

func TestHandlePutPoliciesInvalidJSON(t *testing.T) {
	srv, _, _ := newPoliciesTestServer(t)

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/settings/policies", bytes.NewReader([]byte("not json")))
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

func TestHandlePatchPoliciesPreservesUntouchedFields(t *testing.T) {
	srv, ps, _ := newPoliciesTestServer(t)

	// Seed: set one field.
	seed := ps.Get()
	seed.Bridge.WebUserTTLMinutes = 23
	seed.AgentPolicy.CheckinChannel = "#general"
	if err := ps.Set(seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// PATCH only ChannelResolutions — TTL and CheckinChannel should remain.
	patch := Policies{ChannelResolutions: map[string]string{"#x": "full"}}
	resp := policiesDoJSON(t, srv, http.MethodPatch, "/v1/settings/policies", patch)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	got := ps.Get()
	if got.Bridge.WebUserTTLMinutes != 23 {
		t.Errorf("ttl = %d, want 23 (preserved)", got.Bridge.WebUserTTLMinutes)
	}
	if got.AgentPolicy.CheckinChannel != "#general" {
		t.Errorf("checkin_channel = %q, want #general", got.AgentPolicy.CheckinChannel)
	}
	if got.ChannelResolutions["#x"] != "full" {
		t.Errorf("channel_resolutions[#x] = %q, want full", got.ChannelResolutions["#x"])
	}
}

func TestHandlePatchPoliciesMergesBehaviorEnabled(t *testing.T) {
	srv, ps, _ := newPoliciesTestServer(t)

	patch := Policies{
		Behaviors: []BehaviorConfig{{ID: "scribe", Enabled: true}},
	}
	resp := policiesDoJSON(t, srv, http.MethodPatch, "/v1/settings/policies", patch)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	got := ps.Get()
	var found bool
	for _, b := range got.Behaviors {
		if b.ID == "scribe" {
			found = true
			if !b.Enabled {
				t.Error("scribe should be enabled after patch")
			}
			if b.Name == "" {
				t.Error("scribe should retain default Name from defaults")
			}
		}
	}
	if !found {
		t.Error("scribe not found in behaviors after patch")
	}
}

func TestHandleGetRelayConfigEmpty(t *testing.T) {
	srv, _, _ := newPoliciesTestServer(t)

	resp := policiesDoJSON(t, srv, http.MethodGet, "/v1/relay/config", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var got RelayConfig
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Should have an empty/nil ChannelResolutions and no agent-specific fields.
	if got.AgentPrompt != "" || got.AgentModel != "" {
		t.Errorf("expected empty agent fields, got prompt=%q model=%q", got.AgentPrompt, got.AgentModel)
	}
}

func TestHandleGetRelayConfigChannelResolutions(t *testing.T) {
	srv, ps, _ := newPoliciesTestServer(t)

	p := ps.Get()
	p.ChannelResolutions = map[string]string{"#dev": "actions", "#audit": "full"}
	if err := ps.Set(p); err != nil {
		t.Fatalf("set: %v", err)
	}

	resp := policiesDoJSON(t, srv, http.MethodGet, "/v1/relay/config", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var got RelayConfig
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ChannelResolutions["#dev"] != "actions" {
		t.Errorf("channel_resolutions[#dev] = %q, want actions", got.ChannelResolutions["#dev"])
	}
	if got.ChannelResolutions["#audit"] != "full" {
		t.Errorf("channel_resolutions[#audit] = %q, want full", got.ChannelResolutions["#audit"])
	}
}

func TestHandleGetRelayConfigWithAgentNick(t *testing.T) {
	srv, _, reg := newPoliciesTestServer(t)

	// Register an agent and configure per-agent LLM fields.
	if _, _, err := reg.Register("relay-1", registry.AgentTypeWorker, registry.EngagementConfig{Channels: []string{"#fleet"}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	a, err := reg.Get("relay-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	temp := 0.7
	a.Config.SystemPrompt = "you are relay-1"
	a.Config.Model = "gpt-x"
	a.Config.Temperature = &temp
	a.Config.ToolAllowlist = []string{"web", "shell"}
	if err := reg.Update(a); err != nil {
		t.Fatalf("update: %v", err)
	}

	resp := policiesDoJSON(t, srv, http.MethodGet, "/v1/relay/config?nick=relay-1", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var got RelayConfig
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AgentPrompt != "you are relay-1" {
		t.Errorf("agent_prompt = %q, want %q", got.AgentPrompt, "you are relay-1")
	}
	if got.AgentModel != "gpt-x" {
		t.Errorf("agent_model = %q, want gpt-x", got.AgentModel)
	}
	if got.AgentTemperature == nil || *got.AgentTemperature != 0.7 {
		t.Errorf("agent_temperature = %v, want 0.7", got.AgentTemperature)
	}
	if len(got.AgentToolAllowlist) != 2 || got.AgentToolAllowlist[0] != "web" {
		t.Errorf("agent_tool_allowlist = %v", got.AgentToolAllowlist)
	}
}

func TestHandleGetRelayConfigUnknownAgent(t *testing.T) {
	srv, _, _ := newPoliciesTestServer(t)

	// nick=unknown should not error; just returns empty agent fields.
	resp := policiesDoJSON(t, srv, http.MethodGet, "/v1/relay/config?nick=unknown-relay", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var got RelayConfig
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AgentPrompt != "" || got.AgentModel != "" {
		t.Errorf("expected empty agent fields for unknown nick, got prompt=%q model=%q",
			got.AgentPrompt, got.AgentModel)
	}
}

func TestHandleGetSettingsIncludesPoliciesAndBotCommands(t *testing.T) {
	srv, _, _ := newPoliciesTestServer(t)

	resp := policiesDoJSON(t, srv, http.MethodGet, "/v1/settings", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["tls"]; !ok {
		t.Error("settings missing tls section")
	}
	if _, ok := body["policies"]; !ok {
		t.Error("settings missing policies section")
	}
	bc, ok := body["bot_commands"].(map[string]any)
	if !ok {
		t.Fatal("settings missing bot_commands map")
	}
	if _, ok := bc["oracle"]; !ok {
		t.Error("bot_commands should include oracle")
	}
}

// --- PolicyStore.Merge / OnChange unit coverage ---

func TestPolicyStoreMergeLLMBackends(t *testing.T) {
	ps, err := NewPolicyStore(filepath.Join(t.TempDir(), "policies.json"), 5)
	if err != nil {
		t.Fatalf("NewPolicyStore: %v", err)
	}
	patch := Policies{
		LLMBackends: []PolicyLLMBackend{
			{Name: "anthro", Backend: "anthropic", APIKey: "secret"},
		},
	}
	if err := ps.Merge(patch); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	got := ps.Get()
	if len(got.LLMBackends) != 1 || got.LLMBackends[0].Name != "anthro" {
		t.Errorf("llm_backends = %+v", got.LLMBackends)
	}
}

func TestPolicyStoreOnChangeFires(t *testing.T) {
	ps, err := NewPolicyStore(filepath.Join(t.TempDir(), "policies.json"), 5)
	if err != nil {
		t.Fatalf("NewPolicyStore: %v", err)
	}
	got := make(chan Policies, 1)
	ps.OnChange(func(p Policies) { got <- p })

	p := ps.Get()
	p.AgentPolicy.CheckinChannel = "#updates"
	if err := ps.Set(p); err != nil {
		t.Fatalf("Set: %v", err)
	}

	select {
	case snap := <-got:
		if snap.AgentPolicy.CheckinChannel != "#updates" {
			t.Errorf("snap.checkin_channel = %q, want #updates", snap.AgentPolicy.CheckinChannel)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnChange not invoked within 2s")
	}
}
