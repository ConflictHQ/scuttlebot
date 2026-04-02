package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotAndPrune(t *testing.T) {
	dir := t.TempDir()
	histDir := filepath.Join(dir, "history")
	configPath := filepath.Join(dir, "scuttlebot.yaml")

	// No-op when config file doesn't exist yet.
	if err := SnapshotConfig(histDir, configPath); err != nil {
		t.Fatalf("SnapshotConfig (no file): %v", err)
	}

	// Write a config file and snapshot it.
	if err := os.WriteFile(configPath, []byte("bridge:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotConfig(histDir, configPath); err != nil {
		t.Fatalf("SnapshotConfig: %v", err)
	}

	entries, err := ListHistory(histDir, "scuttlebot.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 snapshot, got %d", len(entries))
	}
	if entries[0].Size == 0 {
		t.Error("snapshot size should be non-zero")
	}
	if entries[0].Timestamp.IsZero() {
		t.Error("snapshot timestamp should be set")
	}
}

func TestPruneHistory(t *testing.T) {
	dir := t.TempDir()
	base := "scuttlebot.yaml"
	keep := 3

	// Write 5 snapshot files with distinct timestamps.
	for i := range 5 {
		stamp := time.Now().Add(time.Duration(i) * time.Second).Format(historyTimestampFormat)
		name := filepath.Join(dir, base+"."+stamp)
		if err := os.WriteFile(name, []byte("v"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Ensure distinct mtime ordering.
		time.Sleep(2 * time.Millisecond)
	}

	if err := PruneHistory(dir, base, keep); err != nil {
		t.Fatal(err)
	}

	entries, err := ListHistory(dir, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != keep {
		t.Errorf("want %d entries after prune, got %d", keep, len(entries))
	}
}

func TestConfigSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scuttlebot.yaml")

	var cfg Config
	cfg.Defaults()
	cfg.Topology.Channels = []StaticChannelConfig{
		{Name: "#general", Topic: "Fleet coordination", Autojoin: []string{"bridge"}},
	}
	cfg.Topology.Types = []ChannelTypeConfig{
		{Name: "task", Prefix: "task.", Ephemeral: true, TTL: Duration{72 * time.Hour}},
	}

	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var loaded Config
	loaded.Defaults()
	if err := loaded.LoadFile(path); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	if len(loaded.Topology.Channels) != 1 || loaded.Topology.Channels[0].Name != "#general" {
		t.Errorf("topology channels not round-tripped: %+v", loaded.Topology.Channels)
	}
	if len(loaded.Topology.Types) != 1 || loaded.Topology.Types[0].Name != "task" {
		t.Errorf("topology types not round-tripped: %+v", loaded.Topology.Types)
	}
	if loaded.Topology.Types[0].TTL.Duration != 72*time.Hour {
		t.Errorf("TTL = %v, want 72h", loaded.Topology.Types[0].TTL.Duration)
	}
}
