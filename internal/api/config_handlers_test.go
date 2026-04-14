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
	srv := httptest.NewServer(New(reg, auth.TestStore("tok"), nil, nil, nil, nil, nil, store, nil, "", false, false, log).Handler())
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

func TestHandlePutConfigAgentPolicy(t *testing.T) {
	srv, store := newCfgTestServer(t)

	update := map[string]any{
		"agent_policy": map[string]any{
			"require_checkin":   true,
			"checkin_channel":   "#fleet",
			"required_channels": []string{"#general"},
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

	got := store.Get()
	if !got.AgentPolicy.RequireCheckin {
		t.Error("agent_policy.require_checkin should be true")
	}
	if got.AgentPolicy.CheckinChannel != "#fleet" {
		t.Errorf("agent_policy.checkin_channel = %q, want #fleet", got.AgentPolicy.CheckinChannel)
	}
	if len(got.AgentPolicy.RequiredChannels) != 1 || got.AgentPolicy.RequiredChannels[0] != "#general" {
		t.Errorf("agent_policy.required_channels = %v", got.AgentPolicy.RequiredChannels)
	}
}

func TestHandlePutConfigLogging(t *testing.T) {
	srv, store := newCfgTestServer(t)

	update := map[string]any{
		"logging": map[string]any{
			"enabled":      true,
			"dir":          "./data/logs",
			"format":       "jsonl",
			"rotation":     "daily",
			"per_channel":  true,
			"max_age_days": 30,
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

	got := store.Get()
	if !got.Logging.Enabled {
		t.Error("logging.enabled should be true")
	}
	if got.Logging.Dir != "./data/logs" {
		t.Errorf("logging.dir = %q, want ./data/logs", got.Logging.Dir)
	}
	if got.Logging.Format != "jsonl" {
		t.Errorf("logging.format = %q, want jsonl", got.Logging.Format)
	}
	if got.Logging.Rotation != "daily" {
		t.Errorf("logging.rotation = %q, want daily", got.Logging.Rotation)
	}
	if !got.Logging.PerChannel {
		t.Error("logging.per_channel should be true")
	}
	if got.Logging.MaxAgeDays != 30 {
		t.Errorf("logging.max_age_days = %d, want 30", got.Logging.MaxAgeDays)
	}
}

func TestHandlePutConfigErgo(t *testing.T) {
	srv, store := newCfgTestServer(t)

	update := map[string]any{
		"ergo": map[string]any{
			"network_name": "testnet",
			"server_name":  "irc.test.local",
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

	// Ergo changes should be flagged as restart_required.
	var result struct {
		Saved           bool     `json:"saved"`
		RestartRequired []string `json:"restart_required"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.Saved {
		t.Error("expected saved=true")
	}
	if len(result.RestartRequired) == 0 {
		t.Error("expected restart_required to be non-empty for ergo changes")
	}

	got := store.Get()
	if got.Ergo.NetworkName != "testnet" {
		t.Errorf("ergo.network_name = %q, want testnet", got.Ergo.NetworkName)
	}
	if got.Ergo.ServerName != "irc.test.local" {
		t.Errorf("ergo.server_name = %q, want irc.test.local", got.Ergo.ServerName)
	}
}

func TestHandlePutConfigTLS(t *testing.T) {
	srv, store := newCfgTestServer(t)

	update := map[string]any{
		"tls": map[string]any{
			"domain":         "example.com",
			"email":          "admin@example.com",
			"allow_insecure": true,
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
		RestartRequired []string `json:"restart_required"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if len(result.RestartRequired) == 0 {
		t.Error("expected restart_required for tls.domain change")
	}

	got := store.Get()
	if got.TLS.Domain != "example.com" {
		t.Errorf("tls.domain = %q, want example.com", got.TLS.Domain)
	}
	if got.TLS.Email != "admin@example.com" {
		t.Errorf("tls.email = %q, want admin@example.com", got.TLS.Email)
	}
	if !got.TLS.AllowInsecure {
		t.Error("tls.allow_insecure should be true")
	}
}

func TestHandleGetConfigIncludesAgentPolicyAndLogging(t *testing.T) {
	srv, store := newCfgTestServer(t)

	cfg := store.Get()
	cfg.AgentPolicy.RequireCheckin = true
	cfg.AgentPolicy.CheckinChannel = "#ops"
	cfg.Logging.Enabled = true
	cfg.Logging.Format = "csv"
	if err := store.Save(cfg); err != nil {
		t.Fatalf("store.Save: %v", err)
	}

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
	ap, ok := body["agent_policy"].(map[string]any)
	if !ok {
		t.Fatal("response missing agent_policy section")
	}
	if ap["require_checkin"] != true {
		t.Error("agent_policy.require_checkin should be true")
	}
	if ap["checkin_channel"] != "#ops" {
		t.Errorf("agent_policy.checkin_channel = %v, want #ops", ap["checkin_channel"])
	}
	lg, ok := body["logging"].(map[string]any)
	if !ok {
		t.Fatal("response missing logging section")
	}
	if lg["enabled"] != true {
		t.Error("logging.enabled should be true")
	}
	if lg["format"] != "csv" {
		t.Errorf("logging.format = %v, want csv", lg["format"])
	}
}

func TestHandleGetConfigHistoryEntry(t *testing.T) {
	srv, store := newCfgTestServer(t)

	// Save twice so a snapshot exists.
	cfg := store.Get()
	cfg.Bridge.WebUserTTLMinutes = 11
	if err := store.Save(cfg); err != nil {
		t.Fatalf("first save: %v", err)
	}
	cfg2 := store.Get()
	cfg2.Bridge.WebUserTTLMinutes = 22
	if err := store.Save(cfg2); err != nil {
		t.Fatalf("second save: %v", err)
	}

	// List history to find a real filename.
	entries, err := store.ListHistory()
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(entries) == 0 {
		t.Skip("no history entries; snapshot may not have been created")
	}
	filename := entries[0].Filename

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/config/history/"+filename, nil)
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
		t.Error("history entry response missing bridge section")
	}
}

func TestHandleGetConfigHistoryEntryNotFound(t *testing.T) {
	srv, _ := newCfgTestServer(t)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/config/history/nonexistent.yaml", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestConfigStoreOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scuttlebot.yaml")

	var cfg config.Config
	cfg.Defaults()
	cfg.Ergo.DataDir = dir
	store := NewConfigStore(path, cfg)

	done := make(chan config.Config, 1)
	store.OnChange(func(c config.Config) { done <- c })

	next := store.Get()
	next.Bridge.WebUserTTLMinutes = 99
	if err := store.Save(next); err != nil {
		t.Fatalf("Save: %v", err)
	}

	select {
	case c := <-done:
		if c.Bridge.WebUserTTLMinutes != 99 {
			t.Errorf("OnChange got TTL=%d, want 99", c.Bridge.WebUserTTLMinutes)
		}
	case <-time.After(2 * time.Second):
		t.Error("OnChange callback not called within timeout")
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
