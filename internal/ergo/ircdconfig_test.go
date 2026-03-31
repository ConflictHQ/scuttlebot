package ergo_test

import (
	"strings"
	"testing"

	"github.com/conflicthq/scuttlebot/internal/config"
	"github.com/conflicthq/scuttlebot/internal/ergo"
)

func TestGenerateConfig(t *testing.T) {
	cfg := config.ErgoConfig{
		NetworkName: "testnet",
		ServerName:  "irc.test.local",
		IRCAddr:     "127.0.0.1:6667",
		DataDir:     "/tmp/ergo",
		APIAddr:     "127.0.0.1:8089",
		APIToken:    "test-token-abc123",
	}

	data, err := ergo.GenerateConfig(cfg)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}

	yaml := string(data)

	cases := []struct {
		field string
		want  string
	}{
		{"network name", "name: testnet"},
		{"server name", "name: irc.test.local"},
		{"irc addr", `"127.0.0.1:6667"`},
		{"data dir", "path: ./ircd.db"},
		{"api addr", `"127.0.0.1:8089"`},
		{"api token", "test-token-abc123"},
		{"api enabled", "enabled: true"},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			if !strings.Contains(yaml, tc.want) {
				t.Errorf("generated config missing %q\ngot:\n%s", tc.want, yaml)
			}
		})
	}
}

func TestGenerateConfigWithPostgresHistory(t *testing.T) {
	cfg := config.ErgoConfig{
		NetworkName: "testnet",
		ServerName:  "irc.test.local",
		IRCAddr:     "127.0.0.1:6667",
		DataDir:     "/tmp/ergo",
		APIAddr:     "127.0.0.1:8089",
		APIToken:    "tok",
		History: config.HistoryConfig{
			Enabled:     true,
			PostgresDSN: "postgres://ergo:pass@localhost/ergo_history",
		},
	}

	data, err := ergo.GenerateConfig(cfg)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}

	yaml := string(data)
	if !strings.Contains(yaml, "enabled: true") {
		t.Error("expected history enabled")
	}
	if !strings.Contains(yaml, "postgres://ergo:pass@localhost/ergo_history") {
		t.Error("expected postgres DSN in config")
	}
}

func TestGenerateConfigNoHistory(t *testing.T) {
	cfg := config.ErgoConfig{
		NetworkName: "testnet",
		ServerName:  "irc.test.local",
		IRCAddr:     "127.0.0.1:6667",
		DataDir:     "/tmp/ergo",
		APIAddr:     "127.0.0.1:8089",
		APIToken:    "tok",
	}

	data, err := ergo.GenerateConfig(cfg)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}

	yaml := string(data)
	if strings.Contains(yaml, "postgres") {
		t.Error("postgres config should not appear when history disabled")
	}
}
