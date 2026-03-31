// Package config defines scuttlebot's configuration schema.
package config

// Config is the top-level scuttlebot configuration.
type Config struct {
	Ergo     ErgoConfig     `yaml:"ergo"`
	Datastore DatastoreConfig `yaml:"datastore"`
}

// ErgoConfig holds settings for the managed Ergo IRC server.
type ErgoConfig struct {
	// BinaryPath is the path to the ergo binary. Defaults to "ergo" (looks in PATH).
	BinaryPath string `yaml:"binary_path"`

	// DataDir is the directory where Ergo stores ircd.db and generated config.
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
}
