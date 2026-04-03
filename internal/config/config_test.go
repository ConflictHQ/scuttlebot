package config

import (
	"encoding/json"
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

func TestDurationJSONRoundTrip(t *testing.T) {
	cases := []struct {
		dur  time.Duration
		want string
	}{
		{72 * time.Hour, `"72h0m0s"`},
		{30 * time.Minute, `"30m0s"`},
		{0, `"0s"`},
	}
	for _, tc := range cases {
		d := Duration{tc.dur}
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("Marshal(%v): %v", tc.dur, err)
		}
		if string(b) != tc.want {
			t.Errorf("Marshal(%v) = %s, want %s", tc.dur, b, tc.want)
		}
		var back Duration
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("Unmarshal(%s): %v", b, err)
		}
		if back.Duration != tc.dur {
			t.Errorf("round-trip(%v): got %v", tc.dur, back.Duration)
		}
	}
}

func TestDurationJSONUnmarshalErrors(t *testing.T) {
	cases := []struct{ input string }{
		{`123`},       // not a quoted string
		{`"notadur"`}, // not parseable
		{`""`},        // empty string
	}
	for _, tc := range cases {
		var d Duration
		if err := json.Unmarshal([]byte(tc.input), &d); err == nil {
			t.Errorf("Unmarshal(%s): expected error, got nil", tc.input)
		}
	}
}

func TestApplyEnv(t *testing.T) {
	cases := []struct {
		envKey string
		check  func(c Config) bool
	}{
		{"SCUTTLEBOT_API_ADDR", func(c Config) bool { return c.APIAddr == ":9999" }},
		{"SCUTTLEBOT_MCP_ADDR", func(c Config) bool { return c.MCPAddr == ":9998" }},
		{"SCUTTLEBOT_DB_DRIVER", func(c Config) bool { return c.Datastore.Driver == "postgres" }},
		{"SCUTTLEBOT_DB_DSN", func(c Config) bool { return c.Datastore.DSN == "postgres://test" }},
		{"SCUTTLEBOT_ERGO_EXTERNAL", func(c Config) bool { return c.Ergo.External }},
		{"SCUTTLEBOT_ERGO_API_ADDR", func(c Config) bool { return c.Ergo.APIAddr == "http://ergo:8089" }},
		{"SCUTTLEBOT_ERGO_API_TOKEN", func(c Config) bool { return c.Ergo.APIToken == "tok123" }},
		{"SCUTTLEBOT_ERGO_IRC_ADDR", func(c Config) bool { return c.Ergo.IRCAddr == "ergo:6667" }},
		{"SCUTTLEBOT_ERGO_NETWORK_NAME", func(c Config) bool { return c.Ergo.NetworkName == "testnet" }},
		{"SCUTTLEBOT_ERGO_SERVER_NAME", func(c Config) bool { return c.Ergo.ServerName == "irc.test.local" }},
	}

	envValues := map[string]string{
		"SCUTTLEBOT_API_ADDR":          ":9999",
		"SCUTTLEBOT_MCP_ADDR":          ":9998",
		"SCUTTLEBOT_DB_DRIVER":         "postgres",
		"SCUTTLEBOT_DB_DSN":            "postgres://test",
		"SCUTTLEBOT_ERGO_EXTERNAL":     "true",
		"SCUTTLEBOT_ERGO_API_ADDR":     "http://ergo:8089",
		"SCUTTLEBOT_ERGO_API_TOKEN":    "tok123",
		"SCUTTLEBOT_ERGO_IRC_ADDR":     "ergo:6667",
		"SCUTTLEBOT_ERGO_NETWORK_NAME": "testnet",
		"SCUTTLEBOT_ERGO_SERVER_NAME":  "irc.test.local",
	}

	for _, tc := range cases {
		t.Run(tc.envKey, func(t *testing.T) {
			t.Setenv(tc.envKey, envValues[tc.envKey])
			var c Config
			c.Defaults()
			c.ApplyEnv()
			if !tc.check(c) {
				t.Errorf("%s=%q did not apply correctly", tc.envKey, envValues[tc.envKey])
			}
		})
	}
}

func TestApplyEnvErgoExternalFalseByDefault(t *testing.T) {
	// SCUTTLEBOT_ERGO_EXTERNAL absent — should not force External=true.
	var c Config
	c.Defaults()
	c.ApplyEnv()
	if c.Ergo.External {
		t.Error("Ergo.External should be false when env var is absent")
	}
}

func TestConfigSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scuttlebot.yaml")

	var orig Config
	orig.Defaults()
	orig.Bridge.WebUserTTLMinutes = 42
	orig.AgentPolicy.RequireCheckin = true
	orig.AgentPolicy.CheckinChannel = "#fleet"
	orig.Logging.Enabled = true
	orig.Logging.Format = "jsonl"

	if err := orig.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var loaded Config
	loaded.Defaults()
	if err := loaded.LoadFile(path); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	if loaded.Bridge.WebUserTTLMinutes != 42 {
		t.Errorf("WebUserTTLMinutes = %d, want 42", loaded.Bridge.WebUserTTLMinutes)
	}
	if !loaded.AgentPolicy.RequireCheckin {
		t.Error("AgentPolicy.RequireCheckin should be true")
	}
	if loaded.AgentPolicy.CheckinChannel != "#fleet" {
		t.Errorf("CheckinChannel = %q, want #fleet", loaded.AgentPolicy.CheckinChannel)
	}
	if !loaded.Logging.Enabled {
		t.Error("Logging.Enabled should be true")
	}
	if loaded.Logging.Format != "jsonl" {
		t.Errorf("Logging.Format = %q, want jsonl", loaded.Logging.Format)
	}
}

func TestLoadFileMissingIsNotError(t *testing.T) {
	var c Config
	c.Defaults()
	if err := c.LoadFile("/nonexistent/path/scuttlebot.yaml"); err != nil {
		t.Errorf("LoadFile on missing file should return nil, got %v", err)
	}
}
