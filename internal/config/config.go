// Package config defines scuttlebot's configuration schema.
package config

import "os"

// Config is the top-level scuttlebot configuration.
type Config struct {
	Ergo      ErgoConfig      `yaml:"ergo"`
	Datastore DatastoreConfig `yaml:"datastore"`

	// APIAddr is the address for scuttlebot's own HTTP management API.
	// Default: ":8080"
	APIAddr string `yaml:"api_addr"`
}

// ErgoConfig holds settings for the managed Ergo IRC server.
type ErgoConfig struct {
	// External disables subprocess management. When true, scuttlebot expects
	// ergo to already be running and reachable at APIAddr and IRCAddr.
	// Use this in Docker/K8s deployments where ergo runs as a separate container.
	External bool `yaml:"external"`

	// BinaryPath is the path to the ergo binary. Defaults to "ergo" (looks in PATH).
	// Unused when External is true.
	BinaryPath string `yaml:"binary_path"`

	// DataDir is the directory where Ergo stores ircd.db and generated config.
	// Unused when External is true.
	DataDir string `yaml:"data_dir"`

	// NetworkName is the human-readable IRC network name.
	NetworkName string `yaml:"network_name"`

	// ServerName is the IRC server hostname (e.g. "irc.example.com").
	ServerName string `yaml:"server_name"`

	// IRCAddr is the address Ergo listens for IRC connections on.
	// Default: "127.0.0.1:6667" (loopback plaintext for private networks).
	IRCAddr string `yaml:"irc_addr"`

	// APIAddr is the address of Ergo's HTTP management API.
	// Default: "127.0.0.1:8089" (loopback only).
	APIAddr string `yaml:"api_addr"`

	// APIToken is the bearer token for Ergo's HTTP API.
	// scuttlebot generates this on first start and stores it.
	APIToken string `yaml:"api_token"`

	// History configures persistent message history storage.
	History HistoryConfig `yaml:"history"`
}

// HistoryConfig configures Ergo's persistent message history.
type HistoryConfig struct {
	// Enabled enables persistent history storage.
	Enabled bool `yaml:"enabled"`

	// PostgresDSN is the Postgres connection string for persistent history.
	// Recommended. If empty and Enabled is true, MySQL config is used instead.
	PostgresDSN string `yaml:"postgres_dsn"`

	// MySQL is the MySQL connection config for persistent history.
	MySQL MySQLConfig `yaml:"mysql"`
}

// MySQLConfig holds MySQL connection settings for Ergo history.
type MySQLConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`
}

// DatastoreConfig configures scuttlebot's own state store (separate from Ergo).
type DatastoreConfig struct {
	// Driver is "sqlite" or "postgres". Default: "sqlite".
	Driver string `yaml:"driver"`

	// DSN is the data source name.
	// For sqlite: path to the .db file.
	// For postgres: connection string.
	DSN string `yaml:"dsn"`
}

// Defaults fills in zero values with sensible defaults.
func (c *Config) Defaults() {
	if c.Ergo.BinaryPath == "" {
		c.Ergo.BinaryPath = "ergo"
	}
	if c.Ergo.DataDir == "" {
		c.Ergo.DataDir = "./data/ergo"
	}
	if c.Ergo.NetworkName == "" {
		c.Ergo.NetworkName = "scuttlebot"
	}
	if c.Ergo.ServerName == "" {
		c.Ergo.ServerName = "irc.scuttlebot.local"
	}
	if c.Ergo.IRCAddr == "" {
		c.Ergo.IRCAddr = "127.0.0.1:6667"
	}
	if c.Ergo.APIAddr == "" {
		c.Ergo.APIAddr = "127.0.0.1:8089"
	}
	if c.Datastore.Driver == "" {
		c.Datastore.Driver = "sqlite"
	}
	if c.Datastore.DSN == "" {
		c.Datastore.DSN = "./data/scuttlebot.db"
	}
	if c.APIAddr == "" {
		c.APIAddr = ":8080"
	}
}

func envStr(key string) string { return os.Getenv(key) }

// ApplyEnv overrides config values with SCUTTLEBOT_* environment variables.
// Call after Defaults() to allow env to override defaults.
//
// Supported variables:
//
//	SCUTTLEBOT_API_ADDR          — scuttlebot HTTP API listen address (e.g. ":8080")
//	SCUTTLEBOT_DB_DRIVER         — "sqlite" or "postgres"
//	SCUTTLEBOT_DB_DSN            — datastore connection string
//	SCUTTLEBOT_ERGO_EXTERNAL     — "true" to skip subprocess management
//	SCUTTLEBOT_ERGO_API_ADDR     — ergo HTTP API address (e.g. "http://ergo:8089")
//	SCUTTLEBOT_ERGO_API_TOKEN    — ergo HTTP API bearer token
//	SCUTTLEBOT_ERGO_IRC_ADDR     — ergo IRC listen/connect address (e.g. "ergo:6667")
//	SCUTTLEBOT_ERGO_NETWORK_NAME — IRC network name
//	SCUTTLEBOT_ERGO_SERVER_NAME  — IRC server hostname
func (c *Config) ApplyEnv() {
	if v := envStr("SCUTTLEBOT_API_ADDR"); v != "" {
		c.APIAddr = v
	}
	if v := envStr("SCUTTLEBOT_DB_DRIVER"); v != "" {
		c.Datastore.Driver = v
	}
	if v := envStr("SCUTTLEBOT_DB_DSN"); v != "" {
		c.Datastore.DSN = v
	}
	if v := envStr("SCUTTLEBOT_ERGO_EXTERNAL"); v == "true" || v == "1" {
		c.Ergo.External = true
	}
	if v := envStr("SCUTTLEBOT_ERGO_API_ADDR"); v != "" {
		c.Ergo.APIAddr = v
	}
	if v := envStr("SCUTTLEBOT_ERGO_API_TOKEN"); v != "" {
		c.Ergo.APIToken = v
	}
	if v := envStr("SCUTTLEBOT_ERGO_IRC_ADDR"); v != "" {
		c.Ergo.IRCAddr = v
	}
	if v := envStr("SCUTTLEBOT_ERGO_NETWORK_NAME"); v != "" {
		c.Ergo.NetworkName = v
	}
	if v := envStr("SCUTTLEBOT_ERGO_SERVER_NAME"); v != "" {
		c.Ergo.ServerName = v
	}
}
