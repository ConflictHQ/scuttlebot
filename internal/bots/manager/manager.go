// Package manager starts and stops system bots based on policy configuration.
package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/conflicthq/scuttlebot/internal/bots/auditbot"
	"github.com/conflicthq/scuttlebot/internal/bots/herald"
	"github.com/conflicthq/scuttlebot/internal/bots/oracle"
	"github.com/conflicthq/scuttlebot/internal/bots/scribe"
	"github.com/conflicthq/scuttlebot/internal/bots/scroll"
	"github.com/conflicthq/scuttlebot/internal/bots/sentinel"
	"github.com/conflicthq/scuttlebot/internal/bots/snitch"
	"github.com/conflicthq/scuttlebot/internal/bots/steward"
	"github.com/conflicthq/scuttlebot/internal/bots/systembot"
	"github.com/conflicthq/scuttlebot/internal/bots/warden"
	"github.com/conflicthq/scuttlebot/internal/llm"
)

// scribeHistoryAdapter adapts scribe.FileStore to oracle.HistoryFetcher.
type scribeHistoryAdapter struct {
	store *scribe.FileStore
}

func (a *scribeHistoryAdapter) Query(channel string, limit int) ([]oracle.HistoryEntry, error) {
	entries, err := a.store.Query(channel, limit)
	if err != nil {
		return nil, err
	}
	out := make([]oracle.HistoryEntry, len(entries))
	for i, e := range entries {
		out[i] = oracle.HistoryEntry{
			Nick:        e.Nick,
			MessageType: e.MessageType,
			Raw:         e.Raw,
		}
	}
	return out, nil
}

// BotSpec mirrors api.BehaviorConfig without importing the api package.
type BotSpec struct {
	ID               string
	Nick             string
	Enabled          bool
	JoinAllChannels  bool
	RequiredChannels []string
	Config           map[string]any
}

// Provisioner can register and change passwords for IRC accounts.
type Provisioner interface {
	RegisterAccount(name, pass string) error
	ChangePassword(name, pass string) error
}

// ChannelLister can enumerate active IRC channels.
type ChannelLister interface {
	ListChannels() ([]string, error)
}

// bot is the common interface all bots satisfy.
type bot interface {
	Start(ctx context.Context) error
}

// Manager starts and stops bots based on BotSpec slices.
type Manager struct {
	ircAddr   string
	dataDir   string
	prov      Provisioner
	channels  ChannelLister
	log       *slog.Logger
	passwords map[string]string         // nick → password, persisted
	running   map[string]context.CancelFunc
}

// New creates a Manager.
func New(ircAddr, dataDir string, prov Provisioner, channels ChannelLister, log *slog.Logger) *Manager {
	m := &Manager{
		ircAddr:   ircAddr,
		dataDir:   dataDir,
		prov:      prov,
		channels:  channels,
		log:       log,
		passwords: make(map[string]string),
		running:   make(map[string]context.CancelFunc),
	}
	_ = m.loadPasswords()
	return m
}

// Running returns the nicks of currently running bots.
func (m *Manager) Running() []string {
	out := make([]string, 0, len(m.running))
	for nick := range m.running {
		out = append(out, nick)
	}
	return out
}

// Sync starts enabled+not-running bots and stops disabled+running bots.
func (m *Manager) Sync(ctx context.Context, specs []BotSpec) {
	desired := make(map[string]BotSpec, len(specs))
	for _, s := range specs {
		desired[s.Nick] = s
	}

	// Stop bots that are running but should be disabled.
	for nick, cancel := range m.running {
		spec, ok := desired[nick]
		if !ok || !spec.Enabled {
			m.log.Info("manager: stopping bot", "nick", nick)
			cancel()
			delete(m.running, nick)
		}
	}

	// Start bots that are enabled but not running.
	for _, spec := range specs {
		if !spec.Enabled {
			continue
		}
		if _, running := m.running[spec.Nick]; running {
			continue
		}

		pass, err := m.ensurePassword(spec.Nick)
		if err != nil {
			m.log.Error("manager: ensure password", "nick", spec.Nick, "err", err)
			continue
		}

		if err := m.ensureAccount(spec.Nick, pass); err != nil {
			m.log.Error("manager: ensure account", "nick", spec.Nick, "err", err)
			continue
		}

		channels, err := m.resolveChannels(spec)
		if err != nil {
			m.log.Warn("manager: list channels failed, using required", "nick", spec.Nick, "err", err)
		}

		b, err := m.buildBot(spec, pass, channels)
		if err != nil {
			m.log.Error("manager: build bot", "nick", spec.Nick, "err", err)
			continue
		}
		if b == nil {
			continue
		}

		botCtx, cancel := context.WithCancel(ctx)
		m.running[spec.Nick] = cancel

		go func(nick string, b bot, ctx context.Context) {
			m.log.Info("manager: starting bot", "nick", nick)
			if err := b.Start(ctx); err != nil && ctx.Err() == nil {
				m.log.Error("manager: bot exited with error", "nick", nick, "err", err)
			}
		}(spec.Nick, b, botCtx)
	}
}

func (m *Manager) resolveChannels(spec BotSpec) ([]string, error) {
	if spec.JoinAllChannels {
		ch, err := m.channels.ListChannels()
		if err != nil {
			return spec.RequiredChannels, err
		}
		return ch, nil
	}
	return spec.RequiredChannels, nil
}

func (m *Manager) ensurePassword(nick string) (string, error) {
	if pass, ok := m.passwords[nick]; ok {
		return pass, nil
	}
	pass, err := genPassword()
	if err != nil {
		return "", err
	}
	m.passwords[nick] = pass
	if err := m.savePasswords(); err != nil {
		return "", err
	}
	return pass, nil
}

func (m *Manager) ensureAccount(nick, pass string) error {
	if err := m.prov.RegisterAccount(nick, pass); err != nil {
		if strings.Contains(err.Error(), "ACCOUNT_EXISTS") {
			return m.prov.ChangePassword(nick, pass)
		}
		return err
	}
	return nil
}

func (m *Manager) buildBot(spec BotSpec, pass string, channels []string) (bot, error) {
	cfg := spec.Config
	switch spec.ID {
	case "scribe":
		store := scribe.NewFileStore(scribe.FileStoreConfig{
			Dir:        cfgStr(cfg, "dir", filepath.Join(m.dataDir, "logs", "scribe")),
			Format:     cfgStr(cfg, "format", "jsonl"),
			Rotation:   cfgStr(cfg, "rotation", "none"),
			MaxSizeMB:  cfgInt(cfg, "max_size_mb", 0),
			PerChannel: cfgBool(cfg, "per_channel", false),
			MaxAgeDays: cfgInt(cfg, "max_age_days", 0),
		})
		return scribe.New(m.ircAddr, pass, channels, store, m.log), nil

	case "auditbot":
		return auditbot.New(m.ircAddr, pass, channels, nil, &auditbot.MemoryStore{}, m.log), nil

	case "snitch":
		return snitch.New(snitch.Config{
			IRCAddr:           m.ircAddr,
			Nick:              spec.Nick,
			Password:          pass,
			AlertChannel:      cfgStr(cfg, "alert_channel", ""),
			AlertNicks:        splitCSV(cfgStr(cfg, "alert_nicks", "")),
			FloodMessages:     cfgInt(cfg, "flood_messages", 10),
			FloodWindow:       time.Duration(cfgInt(cfg, "flood_window_sec", 5)) * time.Second,
			JoinPartThreshold: cfgInt(cfg, "join_part_threshold", 5),
			JoinPartWindow:    time.Duration(cfgInt(cfg, "join_part_window_sec", 30)) * time.Second,
		}, m.log), nil

	case "warden":
		return warden.New(m.ircAddr, pass, nil, warden.ChannelConfig{
			MessagesPerSecond: cfgFloat(cfg, "messages_per_second", 5),
			Burst:             cfgInt(cfg, "burst", 10),
		}, m.log), nil

	case "scroll":
		return scroll.New(m.ircAddr, pass, &scribe.MemoryStore{}, m.log), nil

	case "systembot":
		return systembot.New(m.ircAddr, pass, channels, &systembot.MemoryStore{}, m.log), nil

	case "herald":
		return herald.New(m.ircAddr, pass, herald.RouteConfig{
			DefaultChannel: cfgStr(cfg, "default_channel", ""),
		}, cfgFloat(cfg, "rate_limit", 1), cfgInt(cfg, "burst", 5), m.log), nil

	case "oracle":
		// Resolve API key — prefer direct api_key, fall back to api_key_env for
		// backwards compatibility with existing configs.
		apiKey := cfgStr(cfg, "api_key", "")
		if apiKey == "" {
			apiKeyEnv := cfgStr(cfg, "api_key_env", "")
			if apiKeyEnv != "" {
				apiKey = os.Getenv(apiKeyEnv)
			}
		}

		llmCfg := llm.BackendConfig{
			Backend:      cfgStr(cfg, "backend", "openai"),
			APIKey:       apiKey,
			BaseURL:      cfgStr(cfg, "base_url", ""),
			Model:        cfgStr(cfg, "model", ""),
			Region:       cfgStr(cfg, "region", ""),
			AWSKeyID:     cfgStr(cfg, "aws_key_id", ""),
			AWSSecretKey: cfgStr(cfg, "aws_secret_key", ""),
		}
		provider, err := llm.New(llmCfg)
		if err != nil {
			return nil, fmt.Errorf("oracle: build llm provider: %w", err)
		}

		// Read from the same dir scribe writes to.
		scribeDir := cfgStr(cfg, "scribe_dir", filepath.Join(m.dataDir, "logs", "scribe"))
		fs := scribe.NewFileStore(scribe.FileStoreConfig{Dir: scribeDir, Format: "jsonl"})
		history := &scribeHistoryAdapter{store: fs}

		return oracle.New(m.ircAddr, pass, history, provider, m.log), nil

	case "sentinel":
		apiKey := cfgStr(cfg, "api_key", "")
		if apiKey == "" {
			if env := cfgStr(cfg, "api_key_env", ""); env != "" {
				apiKey = os.Getenv(env)
			}
		}
		llmCfg := llm.BackendConfig{
			Backend:      cfgStr(cfg, "backend", "openai"),
			APIKey:       apiKey,
			BaseURL:      cfgStr(cfg, "base_url", ""),
			Model:        cfgStr(cfg, "model", ""),
			Region:       cfgStr(cfg, "region", ""),
			AWSKeyID:     cfgStr(cfg, "aws_key_id", ""),
			AWSSecretKey: cfgStr(cfg, "aws_secret_key", ""),
		}
		provider, err := llm.New(llmCfg)
		if err != nil {
			return nil, fmt.Errorf("sentinel: build llm provider: %w", err)
		}
		return sentinel.New(sentinel.Config{
			IRCAddr:         m.ircAddr,
			Nick:            spec.Nick,
			Password:        pass,
			ModChannel:      cfgStr(cfg, "mod_channel", "#moderation"),
			DMOperators:     cfgBool(cfg, "dm_operators", false),
			AlertNicks:      splitCSV(cfgStr(cfg, "alert_nicks", "")),
			Policy:          cfgStr(cfg, "policy", ""),
			WindowSize:      cfgInt(cfg, "window_size", 20),
			WindowAge:       time.Duration(cfgInt(cfg, "window_age_sec", 300)) * time.Second,
			CooldownPerNick: time.Duration(cfgInt(cfg, "cooldown_sec", 600)) * time.Second,
			MinSeverity:     cfgStr(cfg, "min_severity", "medium"),
		}, provider, m.log), nil

	case "steward":
		return steward.New(steward.Config{
			IRCAddr:         m.ircAddr,
			Nick:            spec.Nick,
			Password:        pass,
			ModChannel:      cfgStr(cfg, "mod_channel", "#moderation"),
			OperatorNicks:   splitCSV(cfgStr(cfg, "operator_nicks", "")),
			DMOnAction:      cfgBool(cfg, "dm_on_action", false),
			AutoAct:         cfgBool(cfg, "auto_act", true),
			MuteDuration:    time.Duration(cfgInt(cfg, "mute_duration_sec", 600)) * time.Second,
			WarnOnLow:       cfgBool(cfg, "warn_on_low", true),
			CooldownPerNick: time.Duration(cfgInt(cfg, "cooldown_sec", 300)) * time.Second,
		}, m.log), nil

	default:
		return nil, fmt.Errorf("unknown bot ID %q", spec.ID)
	}
}

// passwordsPath returns the path for the passwords file.
func (m *Manager) passwordsPath() string {
	return filepath.Join(m.dataDir, "bot_passwords.json")
}

func (m *Manager) loadPasswords() error {
	raw, err := os.ReadFile(m.passwordsPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, &m.passwords)
}

func (m *Manager) savePasswords() error {
	raw, err := json.MarshalIndent(m.passwords, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.dataDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(m.passwordsPath(), raw, 0600)
}

func genPassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("manager: generate password: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// splitCSV splits a comma-separated string into a slice, trimming spaces and
// filtering empty strings.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Config helper extractors.

func cfgStr(cfg map[string]any, key, def string) string {
	if cfg == nil {
		return def
	}
	v, ok := cfg[key]
	if !ok {
		return def
	}
	s, ok := v.(string)
	if !ok {
		return def
	}
	return s
}

func cfgInt(cfg map[string]any, key string, def int) int {
	if cfg == nil {
		return def
	}
	v, ok := cfg[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return def
}

func cfgBool(cfg map[string]any, key string, def bool) bool {
	if cfg == nil {
		return def
	}
	v, ok := cfg[key]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

func cfgFloat(cfg map[string]any, key string, def float64) float64 {
	if cfg == nil {
		return def
	}
	v, ok := cfg[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return def
}
