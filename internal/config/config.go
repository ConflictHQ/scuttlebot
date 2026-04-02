// Package config defines scuttlebot's configuration schema.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level scuttlebot configuration.
type Config struct {
	Ergo      ErgoConfig          `yaml:"ergo"`
	Datastore DatastoreConfig     `yaml:"datastore"`
	Bridge    BridgeConfig        `yaml:"bridge"`
	TLS       TLSConfig           `yaml:"tls"`
	LLM       LLMConfig           `yaml:"llm"`
	Topology  TopologyConfig      `yaml:"topology"`
	History   ConfigHistoryConfig `yaml:"config_history"`

	// APIAddr is the address for scuttlebot's own HTTP management API.
	// Ignored when TLS.Domain is set (HTTPS runs on :443, HTTP on :80).
	// Default: ":8080"
	APIAddr string `yaml:"api_addr"`

	// MCPAddr is the address for the MCP server.
	// Default: ":8081"
	MCPAddr string `yaml:"mcp_addr"`
}

// ConfigHistoryConfig controls config write-back history retention.
type ConfigHistoryConfig struct {
	// Keep is the number of config snapshots to retain in Dir.
	// 0 disables history. Default: 20.
	Keep int `yaml:"keep"`

	// Dir is the directory for config snapshots.
	// Default: {ergo.data_dir}/config-history
	Dir string `yaml:"dir"`
}

// LLMConfig configures the omnibus LLM gateway used by oracle and any other
// bot or service that needs language model access.
type LLMConfig struct {
	// Backends is the list of configured LLM backends.
	// Each backend has a unique Name used to reference it from bot configs.
	Backends []LLMBackendConfig `yaml:"backends"`
}

// LLMBackendConfig configures a single LLM backend instance.
type LLMBackendConfig struct {
	// Name is a unique identifier for this backend (e.g. "openai-main", "local-ollama").
	// Used when referencing the backend from bot configs.
	Name string `yaml:"name"`

	// Backend is the provider type. Supported values:
	//   Native: anthropic, gemini, bedrock, ollama
	//   OpenAI-compatible: openai, openrouter, together, groq, fireworks, mistral,
	//     ai21, huggingface, deepseek, cerebras, xai,
	//     litellm, lmstudio, jan, localai, vllm, anythingllm
	Backend string `yaml:"backend"`

	// APIKey is the authentication key for cloud backends.
	APIKey string `yaml:"api_key"`

	// BaseURL overrides the default base URL for OpenAI-compatible backends.
	// Required for custom self-hosted endpoints without a known default.
	BaseURL string `yaml:"base_url"`

	// Model is the default model ID. If empty, the first discovered model
	// that passes the allow/block filter is used.
	Model string `yaml:"model"`

	// Region is the AWS region (e.g. "us-east-1"). Bedrock only.
	Region string `yaml:"region"`

	// AWSKeyID is the AWS access key ID. Bedrock only.
	AWSKeyID string `yaml:"aws_key_id"`

	// AWSSecretKey is the AWS secret access key. Bedrock only.
	AWSSecretKey string `yaml:"aws_secret_key"`

	// Allow is a list of regex patterns. If non-empty, only model IDs matching
	// at least one pattern are returned by model discovery.
	Allow []string `yaml:"allow"`

	// Block is a list of regex patterns. Matching model IDs are excluded
	// from model discovery results.
	Block []string `yaml:"block"`

	// Default marks this backend as the one used when no backend is specified
	// in a bot's config. Only one backend should have Default: true.
	Default bool `yaml:"default"`
}

// TLSConfig configures automatic HTTPS via Let's Encrypt.
type TLSConfig struct {
	// Domain enables TLS. When set, scuttlebot obtains a certificate from
	// Let's Encrypt for this domain and serves HTTPS on :443.
	Domain string `yaml:"domain"`

	// Email is sent to Let's Encrypt for certificate expiry notifications.
	Email string `yaml:"email"`

	// CertDir is the directory for the certificate cache.
	// Default: {Ergo.DataDir}/certs
	CertDir string `yaml:"cert_dir"`

	// AllowInsecure keeps plain HTTP running on :80 alongside HTTPS.
	// The ACME HTTP-01 challenge always runs on :80 regardless.
	// Default: true
	AllowInsecure bool `yaml:"allow_insecure"`
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

// BridgeConfig configures the IRC bridge bot that powers the web chat UI.
type BridgeConfig struct {
	// Enabled controls whether the bridge bot starts. Default: true.
	Enabled bool `yaml:"enabled"`

	// Nick is the IRC nick for the bridge bot. Default: "bridge".
	Nick string `yaml:"nick"`

	// Password is the SASL PLAIN passphrase for the bridge's NickServ account.
	// Auto-generated on first start if empty.
	Password string `yaml:"password"`

	// Channels is the list of IRC channels the bridge joins on startup.
	Channels []string `yaml:"channels"`

	// BufferSize is the number of messages to keep per channel. Default: 200.
	BufferSize int `yaml:"buffer_size"`

	// WebUserTTLMinutes controls how long HTTP bridge sender nicks remain visible
	// in the channel user list after their last post. Default: 5.
	WebUserTTLMinutes int `yaml:"web_user_ttl_minutes"`
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

// TopologyConfig is the top-level channel topology declaration.
// It defines static channels provisioned at startup and dynamic channel type
// rules applied when agents create channels at runtime.
type TopologyConfig struct {
	// Nick is the IRC nick used by the topology manager to provision channels
	// via ChanServ. Defaults to "topology".
	Nick string `yaml:"nick"`

	// Channels are static channels provisioned at daemon startup.
	Channels []StaticChannelConfig `yaml:"channels"`

	// Types are prefix-based rules applied to dynamically created channels.
	// The first matching prefix wins.
	Types []ChannelTypeConfig `yaml:"types"`
}

// StaticChannelConfig describes a channel that is provisioned at startup.
type StaticChannelConfig struct {
	// Name is the full channel name including the # prefix (e.g. "#general").
	Name string `yaml:"name"`

	// Topic is the initial channel topic.
	Topic string `yaml:"topic"`

	// Ops is a list of nicks to grant channel operator (+o) access.
	Ops []string `yaml:"ops"`

	// Voice is a list of nicks to grant voice (+v) access.
	Voice []string `yaml:"voice"`

	// Autojoin is a list of bot nicks to invite when the channel is provisioned.
	Autojoin []string `yaml:"autojoin"`
}

// ChannelTypeConfig defines policy rules for a class of dynamically created channels.
// Matched by prefix against channel names (e.g. prefix "task." matches "#task.gh-42").
type ChannelTypeConfig struct {
	// Name is a human-readable type identifier (e.g. "task", "sprint", "incident").
	Name string `yaml:"name"`

	// Prefix is matched against channel names after stripping the leading #.
	// The first matching type wins. (e.g. "task." matches "#task.gh-42")
	Prefix string `yaml:"prefix"`

	// Autojoin is a list of bot nicks to invite when a channel of this type is created.
	Autojoin []string `yaml:"autojoin"`

	// Supervision is the coordination channel where summaries should surface.
	// Agents receive this when they create a channel so they know where to also post.
	// May be a static channel name (e.g. "#general") or a type prefix pattern
	// (e.g. "sprint." — resolved to the most recently created matching channel).
	Supervision string `yaml:"supervision"`

	// Ephemeral marks channels of this type for automatic cleanup.
	Ephemeral bool `yaml:"ephemeral"`

	// TTL is the maximum lifetime of an ephemeral channel with no non-bot members.
	// Zero means no TTL; cleanup only occurs when the channel is empty.
	TTL Duration `yaml:"ttl"`
}

// Duration wraps time.Duration for YAML unmarshalling ("72h", "30m", etc.).
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("config: invalid duration %q: %w", s, err)
	}
	d.Duration = dur
	return nil
}

// MarshalYAML encodes Duration as a human-readable string ("72h", "30m").
func (d Duration) MarshalYAML() (any, error) {
	if d.Duration == 0 {
		return "0s", nil
	}
	return d.Duration.String(), nil
}

// Save marshals c to YAML and writes it to path atomically (write to a temp
// file in the same directory, then rename). Comments in the original file are
// not preserved after the first save.
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	// Write to a sibling temp file then rename for atomic replacement.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("config: rename %s → %s: %w", tmp, path, err)
	}
	return nil
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
	if c.MCPAddr == "" {
		c.MCPAddr = ":8081"
	}
	if !c.Bridge.Enabled && c.Bridge.Nick == "" {
		c.Bridge.Enabled = true // enabled by default
	}
	if c.TLS.Domain != "" && !c.TLS.AllowInsecure {
		c.TLS.AllowInsecure = true // HTTP always on by default
	}
	if c.Bridge.Nick == "" {
		c.Bridge.Nick = "bridge"
	}
	if c.Bridge.BufferSize == 0 {
		c.Bridge.BufferSize = 200
	}
	if c.Bridge.WebUserTTLMinutes == 0 {
		c.Bridge.WebUserTTLMinutes = 5
	}
	if c.Topology.Nick == "" {
		c.Topology.Nick = "topology"
	}
	if c.History.Keep == 0 {
		c.History.Keep = 20
	}
}

func envStr(key string) string { return os.Getenv(key) }

// LoadFile reads a YAML config file into c. Missing file is not an error —
// returns nil so callers can treat an absent config file as "use defaults".
// Call Defaults() first, then LoadFile(), then ApplyEnv() so that file values
// override defaults and env values override the file.
func (c *Config) LoadFile(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	return c.LoadFromBytes(data)
}

// LoadFromBytes parses YAML config bytes into c.
func (c *Config) LoadFromBytes(data []byte) error {
	if err := yaml.Unmarshal(data, c); err != nil {
		return fmt.Errorf("config: parse: %w", err)
	}
	return nil
}

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
	if v := envStr("SCUTTLEBOT_MCP_ADDR"); v != "" {
		c.MCPAddr = v
	}
}
