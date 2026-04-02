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

	"github.com/conflicthq/scuttlebot/internal/config"
	"github.com/conflicthq/scuttlebot/internal/registry"
)

func newCfgTestServer(t *testing.T) (*httptest.Server, *ConfigStore) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "scuttlebot.yaml")

	var cfg config.Config
	cfg.Defaults()
	cfg.Ergo.DataDir = dir

	store := NewConfigStore(path, cfg)
	reg := registry.New(nil, []byte("key"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(reg, []string{"tok"}, nil, nil, nil, nil, nil, store, "", log).Handler())
	t.Cleanup(srv.Close)
	return srv, store
}

func TestHandleGetConfig(t *testing.T) {
	srv, _ := newCfgTestServer(t)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/config", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["bridge"]; !ok {
		t.Error("response missing bridge section")
	}
	if _, ok := body["topology"]; !ok {
		t.Error("response missing topology section")
	}
}

func TestHandlePutConfig(t *testing.T) {
	srv, store := newCfgTestServer(t)

	update := map[string]any{
		"bridge": map[string]any{
			"web_user_ttl_minutes": 10,
		},
		"topology": map[string]any{
			"nick": "topo-bot",
			"channels": []map[string]any{
				{"name": "#general", "topic": "Fleet"},
			},
		},
	}
	body, _ := json.Marshal(update)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/config", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var result struct {
		Saved bool `json:"saved"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.Saved {
		t.Error("expected saved=true")
	}

	// Verify in-memory state updated.
	got := store.Get()
	if got.Bridge.WebUserTTLMinutes != 10 {
		t.Errorf("bridge.web_user_ttl_minutes = %d, want 10", got.Bridge.WebUserTTLMinutes)
	}
	if got.Topology.Nick != "topo-bot" {
		t.Errorf("topology.nick = %q, want topo-bot", got.Topology.Nick)
	}
	if len(got.Topology.Channels) != 1 || got.Topology.Channels[0].Name != "#general" {
		t.Errorf("topology.channels = %+v", got.Topology.Channels)
	}
}

func TestHandleGetConfigHistory(t *testing.T) {
	srv, store := newCfgTestServer(t)

	// Trigger a save to create a snapshot.
	cfg := store.Get()
	cfg.Bridge.WebUserTTLMinutes = 7
	if err := store.Save(cfg); err != nil {
		t.Fatalf("store.Save: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/config/history", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var result struct {
		Entries []config.HistoryEntry `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	// Save creates a snapshot of the *current* file before writing, but the
	// config file didn't exist yet, so no snapshot is created. Second save creates one.
	cfg2 := store.Get()
	cfg2.Bridge.WebUserTTLMinutes = 9
	if err := store.Save(cfg2); err != nil {
		t.Fatalf("store.Save 2: %v", err)
	}

	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/config/history", nil)
	req2.Header.Set("Authorization", "Bearer tok")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	var result2 struct {
		Entries []config.HistoryEntry `json:"entries"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&result2); err != nil {
		t.Fatal(err)
	}
	if len(result2.Entries) < 1 {
		t.Errorf("want ≥1 history entries, got %d", len(result2.Entries))
	}
}
