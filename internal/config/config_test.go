package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTopologyConfigParse(t *testing.T) {
	yaml := `
topology:
  channels:
    - name: "#general"
      topic: "Fleet coordination"
      ops: [bridge, oracle]
      autojoin: [bridge, oracle, scribe]
    - name: "#alerts"
      autojoin: [bridge, sentinel]
  types:
    - name: task
      prefix: "task."
      autojoin: [bridge, scribe]
      supervision: "#general"
      ephemeral: true
      ttl: 72h
    - name: sprint
      prefix: "sprint."
      autojoin: [bridge, oracle, herald]
    - name: incident
      prefix: "incident."
      autojoin: [bridge, sentinel, steward]
      supervision: "#alerts"
      ephemeral: true
      ttl: 168h
`
	f := filepath.Join(t.TempDir(), "scuttlebot.yaml")
	if err := os.WriteFile(f, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	var cfg Config
	cfg.Defaults()
	if err := cfg.LoadFile(f); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	top := cfg.Topology

	// static channels
	if len(top.Channels) != 2 {
		t.Fatalf("want 2 static channels, got %d", len(top.Channels))
	}
	general := top.Channels[0]
	if general.Name != "#general" {
		t.Errorf("channel[0].Name = %q, want #general", general.Name)
	}
	if general.Topic != "Fleet coordination" {
		t.Errorf("channel[0].Topic = %q", general.Topic)
	}
	if len(general.Autojoin) != 3 {
		t.Errorf("channel[0].Autojoin len = %d, want 3", len(general.Autojoin))
	}

	// types
	if len(top.Types) != 3 {
		t.Fatalf("want 3 types, got %d", len(top.Types))
	}
	task := top.Types[0]
	if task.Name != "task" {
		t.Errorf("types[0].Name = %q, want task", task.Name)
	}
	if task.Prefix != "task." {
		t.Errorf("types[0].Prefix = %q, want task.", task.Prefix)
	}
	if task.Supervision != "#general" {
		t.Errorf("types[0].Supervision = %q, want #general", task.Supervision)
	}
	if !task.Ephemeral {
		t.Error("types[0].Ephemeral should be true")
	}
	if task.TTL.Duration != 72*time.Hour {
		t.Errorf("types[0].TTL = %v, want 72h", task.TTL.Duration)
	}

	incident := top.Types[2]
	if incident.TTL.Duration != 168*time.Hour {
		t.Errorf("types[2].TTL = %v, want 168h", incident.TTL.Duration)
	}
}

func TestTopologyConfigEmpty(t *testing.T) {
	yaml := `bridge:
  enabled: true
`
	f := filepath.Join(t.TempDir(), "scuttlebot.yaml")
	if err := os.WriteFile(f, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	var cfg Config
	cfg.Defaults()
	if err := cfg.LoadFile(f); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	// No topology section — should be zero value, not an error.
	if len(cfg.Topology.Channels) != 0 {
		t.Errorf("expected no static channels, got %d", len(cfg.Topology.Channels))
	}
	if len(cfg.Topology.Types) != 0 {
		t.Errorf("expected no types, got %d", len(cfg.Topology.Types))
	}
}

func TestDurationUnmarshal(t *testing.T) {
	cases := []struct {
		input string
		want  time.Duration
	}{
		{"72h", 72 * time.Hour},
		{"30m", 30 * time.Minute},
		{"168h", 168 * time.Hour},
		{"0s", 0},
	}
	for _, tc := range cases {
		yaml := `topology:
  types:
    - name: x
      prefix: "x."
      ttl: ` + tc.input + "\n"
		f := filepath.Join(t.TempDir(), "cfg.yaml")
		if err := os.WriteFile(f, []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		var cfg Config
		if err := cfg.LoadFile(f); err != nil {
			t.Fatalf("input %q: %v", tc.input, err)
		}
		got := cfg.Topology.Types[0].TTL.Duration
		if got != tc.want {
			t.Errorf("input %q: got %v, want %v", tc.input, got, tc.want)
		}
	}
}
