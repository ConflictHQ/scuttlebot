package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/internal/config"
	"github.com/conflicthq/scuttlebot/internal/registry"
	"github.com/conflicthq/scuttlebot/internal/topology"
)

// stubTopologyManager implements topologyManager for tests.
// It records the last ProvisionChannel call and returns a canned Policy.
type stubTopologyManager struct {
	last    topology.ChannelConfig
	policy  *topology.Policy
	provErr error
}

func (s *stubTopologyManager) ProvisionChannel(ch topology.ChannelConfig) error {
	s.last = ch
	return s.provErr
}

func (s *stubTopologyManager) DropChannel(_ string) {}

func (s *stubTopologyManager) Policy() *topology.Policy { return s.policy }

func newTopoTestServer(t *testing.T, topo *stubTopologyManager) (*httptest.Server, string) {
	t.Helper()
	reg := registry.New(nil, []byte("key"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(reg, []string{"tok"}, nil, nil, nil, nil, topo, nil, "", log).Handler())
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
	resp, _ := http.DefaultClient.Do(req)
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
