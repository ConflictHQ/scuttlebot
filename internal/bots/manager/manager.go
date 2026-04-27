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
	"sync"
	"time"

	"github.com/conflicthq/scuttlebot/internal/bots/auditbot"
	"github.com/conflicthq/scuttlebot/internal/bots/herald"
	"github.com/conflicthq/scuttlebot/internal/bots/oracle"
	"github.com/conflicthq/scuttlebot/internal/bots/scribe"
	"github.com/conflicthq/scuttlebot/internal/bots/scroll"
	"github.com/conflicthq/scuttlebot/internal/bots/sentinel"
	"github.com/conflicthq/scuttlebot/internal/bots/shepherd"
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

// channelJoiner is implemented by bots that can join an additional channel
// at runtime without a restart. Used by Manager.NotifyChannelProvisioned.
// Bots that don't satisfy this interface will pick up the new channel on
// their next restart (they re-resolve via ChannelLister at Start).
type channelJoiner interface {
	JoinChannel(channel string)
}

// channelLeaver is implemented by bots that can leave a channel at runtime.
type channelLeaver interface {
	LeaveChannel(channel string)
}

// runningBot tracks a live bot instance so the manager can fan out runtime
// channel changes to it without a restart when the bot supports the
// channelJoiner / channelLeaver interfaces.
type runningBot struct {
	cancel context.CancelFunc
	bot    bot
	spec   BotSpec
}

// Manager starts and stops bots based on BotSpec slices.
type Manager struct {
	ircAddr   string
	dataDir   string
	prov      Provisioner
	channels  ChannelLister
	log       *slog.Logger
	mu        sync.Mutex
	passwords map[string]string // nick → password, persisted
	running   map[string]runningBot
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
		running:   make(map[string]runningBot),
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
	for nick, rb := range m.running {
		spec, ok := desired[nick]
		if !ok || !spec.Enabled {
			m.log.Info("manager: stopping bot", "nick", nick)
			rb.cancel()
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
		m.running[spec.Nick] = runningBot{cancel: cancel, bot: b, spec: spec}

		go func(nick string, b bot, ctx context.Context, currentSpec BotSpec) {
			backoff := 5 * time.Second
			const maxBackoff = 5 * time.Minute
			// Track consecutive auth-shaped failures so we can recover from
			// stale-credentials lock-out instead of looping forever (#174).
			authFailures := 0
			for {
				m.log.Info("manager: starting bot", "nick", nick)
				err := b.Start(ctx)
				if ctx.Err() != nil {
					return // context cancelled — intentional shutdown
				}
				if err != nil {
					m.log.Error("manager: bot exited with error, restarting", "nick", nick, "err", err, "backoff", backoff)
					if isAuthFailure(err) {
						authFailures++
						if authFailures >= 3 {
							// Three consecutive auth failures — credentials may
							// have drifted from what NickServ has on disk.
							// Rotate the password and rebuild the bot so it
							// starts with fresh creds. Reset the counter.
							m.log.Warn("manager: rotating bot credentials after repeated auth failures", "nick", nick, "failures", authFailures)
							if newPass, perr := m.rotatePassword(nick); perr == nil {
								if newBot, berr := m.buildBot(currentSpec, newPass, nil); berr == nil && newBot != nil {
									b = newBot
									m.mu.Lock()
									if rb, ok := m.running[nick]; ok {
										rb.bot = newBot
										m.running[nick] = rb
									}
									m.mu.Unlock()
								}
							}
							authFailures = 0
						}
					} else {
						authFailures = 0 // unrelated error — reset counter
					}
				} else {
					m.log.Info("manager: bot exited cleanly, restarting", "nick", nick, "backoff", backoff)
					authFailures = 0
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
		}(spec.Nick, b, botCtx, spec)
	}
}

// NotifyChannelProvisioned tells the manager that a new channel has been
// provisioned at runtime. Every running bot with JoinAllChannels=true that
// implements channelJoiner is told to join immediately. Bots without the
// interface will pick up the channel on their next restart (they re-resolve
// via ChannelLister at Start). See #162.
func (m *Manager) NotifyChannelProvisioned(channel string) {
	if channel == "" {
		return
	}
	if !strings.HasPrefix(channel, "#") {
		channel = "#" + channel
	}
	for nick, rb := range m.running {
		if !rb.spec.JoinAllChannels {
			continue
		}
		if joiner, ok := rb.bot.(channelJoiner); ok {
			m.log.Info("manager: bot joining new channel", "nick", nick, "channel", channel)
			joiner.JoinChannel(channel)
		}
	}
}

// NotifyChannelDropped tells the manager that a channel was dropped at
// runtime. Mirror of NotifyChannelProvisioned.
func (m *Manager) NotifyChannelDropped(channel string) {
	if channel == "" {
		return
	}
	if !strings.HasPrefix(channel, "#") {
		channel = "#" + channel
	}
	for nick, rb := range m.running {
		if !rb.spec.JoinAllChannels {
			continue
		}
		if leaver, ok := rb.bot.(channelLeaver); ok {
			m.log.Info("manager: bot leaving dropped channel", "nick", nick, "channel", channel)
			leaver.LeaveChannel(channel)
		}
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
	m.mu.Lock()
	if pass, ok := m.passwords[nick]; ok {
		m.mu.Unlock()
		return pass, nil
	}
	m.mu.Unlock()
	pass, err := genPassword()
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.passwords[nick] = pass
	m.mu.Unlock()
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

// rotatePassword generates a fresh password, sets it on NickServ via the
// provisioner, and persists. Used by the bot-restart loop after repeated
// auth failures suggest the cached credential drifted from server state
// (#174). Returns the new password.
func (m *Manager) rotatePassword(nick string) (string, error) {
	pass, err := genPassword()
	if err != nil {
		return "", err
	}
	if err := m.prov.ChangePassword(nick, pass); err != nil {
		return "", fmt.Errorf("manager: rotate password: %w", err)
	}
	m.mu.Lock()
	m.passwords[nick] = pass
	m.mu.Unlock()
	if err := m.savePasswords(); err != nil {
		m.log.Warn("manager: persist rotated password failed", "nick", nick, "err", err)
	}
	return pass, nil
}

// isAuthFailure returns true if err looks like a SASL/account auth problem
// rather than an ordinary network blip. Heuristic — girc surfaces these
// errors as plain strings, so we string-match common patterns.
func isAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "sasl"):
		return true
	case strings.Contains(s, "authentication failed"):
		return true
	case strings.Contains(s, "invalid password"):
		return true
	case strings.Contains(s, "account does not exist"):
		return true
	case strings.Contains(s, "nick collision") && strings.Contains(s, "registered"):
		return true
	}
	return false
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
		bot := auditbot.New(m.ircAddr, pass, channels, nil, &auditbot.MemoryStore{}, m.log)
		bot.SetThrottle(buildAuditThrottle(cfg))
		return bot, nil

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
			MonitorNicks:      splitCSV(cfgStr(cfg, "monitor_nicks", "")), // #169
			Channels:          channels,
		}, m.log), nil

	case "warden":
		return warden.New(m.ircAddr, pass, channels, nil, warden.ChannelConfig{
			MessagesPerSecond: cfgFloat(cfg, "messages_per_second", 5),
			Burst:             cfgInt(cfg, "burst", 10),
			CoolDown:          time.Duration(cfgInt(cfg, "cooldown_sec", 60)) * time.Second, // #171
		}, m.log), nil

	case "scroll":
		// Persistent backing so replay still works when CHATHISTORY isn't
		// available on the server. Reuses scribe's JSONL FileStore (#166).
		scrollDir := cfgStr(cfg, "dir", filepath.Join(m.dataDir, "logs", "scroll"))
		return scroll.NewWithNick(spec.Nick, m.ircAddr, pass, channels, scribe.NewFileStore(scribe.FileStoreConfig{
			Dir:    scrollDir,
			Format: "jsonl",
		}), m.log), nil

	case "systembot":
		// Persistent backing so IRC-system events survive bot restart (#167).
		systemDir := cfgStr(cfg, "dir", filepath.Join(m.dataDir, "logs", "systembot"))
		return systembot.New(m.ircAddr, pass, channels, systembot.NewFileStore(systemDir), m.log), nil

	case "herald":
		// Per-event-type routes are configured as a CSV of "prefix:channel"
		// pairs in the BehaviorConfig (e.g. "github:#dev,deploy:#ops"). The
		// DefaultChannel acts as a fallback when no prefix matches. See #170.
		return herald.New(m.ircAddr, pass, channels, herald.RouteConfig{
			DefaultChannel: cfgStr(cfg, "default_channel", ""),
			Routes:         parseRoutes(cfgStr(cfg, "routes", "")),
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

		return oracle.NewWithNick(spec.Nick, m.ircAddr, pass, channels, history, provider, m.log), nil

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
			Channels:        channels,
		}, provider, m.log), nil

	case "steward":
		return steward.New(steward.Config{
			IRCAddr:            m.ircAddr,
			Nick:               spec.Nick,
			Password:           pass,
			ModChannel:         cfgStr(cfg, "mod_channel", "#moderation"),
			OperatorNicks:      splitCSV(cfgStr(cfg, "operator_nicks", "")),
			DMOnAction:         cfgBool(cfg, "dm_on_action", false),
			AutoAct:            cfgBool(cfg, "auto_act", true),
			MuteDuration:       time.Duration(cfgInt(cfg, "mute_duration_sec", 600)) * time.Second,
			SilenceLowWarnings: cfgBool(cfg, "silence_low_warnings", false),
			CooldownPerNick:    time.Duration(cfgInt(cfg, "cooldown_sec", 300)) * time.Second,
			Channels:           channels,
		}, m.log), nil

	case "shepherd":
		apiKey := cfgStr(cfg, "api_key", "")
		if apiKey == "" {
			if env := cfgStr(cfg, "api_key_env", ""); env != "" {
				apiKey = os.Getenv(env)
			}
		}
		var provider shepherd.LLMProvider
		if apiKey != "" {
			llmCfg := llm.BackendConfig{
				Backend:      cfgStr(cfg, "backend", "openai"),
				APIKey:       apiKey,
				BaseURL:      cfgStr(cfg, "base_url", ""),
				Model:        cfgStr(cfg, "model", ""),
				Region:       cfgStr(cfg, "region", ""),
				AWSKeyID:     cfgStr(cfg, "aws_key_id", ""),
				AWSSecretKey: cfgStr(cfg, "aws_secret_key", ""),
			}
			p, err := llm.New(llmCfg)
			if err != nil {
				return nil, fmt.Errorf("shepherd: build llm provider: %w", err)
			}
			provider = p
		}
		checkinSec := cfgInt(cfg, "checkin_interval_sec", 0)
		return shepherd.New(shepherd.Config{
			IRCAddr:         m.ircAddr,
			Nick:            spec.Nick,
			Password:        pass,
			Channels:        channels,
			ReportChannel:   cfgStr(cfg, "report_channel", "#ops"),
			CheckinInterval: time.Duration(checkinSec) * time.Second,
			StatePath:       cfgStr(cfg, "state_path", filepath.Join(m.dataDir, "shepherd.json")), // #175
		}, provider, m.log), nil

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

// parseRoutes parses a herald route map from a CSV of "prefix:channel" pairs.
// Empty input returns nil. Malformed entries (no colon) are skipped silently.
// e.g. "github:#dev, deploy:#ops" → {"github": "#dev", "deploy": "#ops"}.
func parseRoutes(s string) map[string]string {
	if s == "" {
		return nil
	}
	out := map[string]string{}
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		idx := strings.Index(item, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(item[:idx])
		val := strings.TrimSpace(item[idx+1:])
		if key == "" || val == "" {
			continue
		}
		out[key] = val
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

// buildAuditThrottle assembles the auditbot throttle from a behavior Config
// map. Recognised keys (all optional):
//
//	throttle_window_sec       int   — window size for all presence rules; default 300
//	throttle_join_max         int   — max user.join events per window; default 60; 0 = uncapped
//	throttle_part_max         int   — max user.part events per window; default 60; 0 = uncapped
//	throttle_quit_max         int   — max user.quit events per window; default 60; 0 = uncapped
//	throttle_nick_max         int   — max user.nick events per window; default 60; 0 = uncapped
//	throttle_kick_max         int   — max user.kick events per window; default 0 = uncapped
//
// Operators who want to disable presence throttling entirely set every
// throttle_*_max to 0.
func buildAuditThrottle(cfg map[string]any) auditbot.ThrottleConfig {
	windowSec := cfgInt(cfg, "throttle_window_sec", 300)
	if windowSec <= 0 {
		windowSec = 300
	}
	window := time.Duration(windowSec) * time.Second

	rule := func(key string, def int) auditbot.ThrottleRule {
		max := cfgInt(cfg, key, def)
		if max < 0 {
			max = 0
		}
		return auditbot.ThrottleRule{Max: max, Window: window}
	}

	return auditbot.ThrottleConfig{
		PerType: map[string]auditbot.ThrottleRule{
			auditbot.EventUserJoin: rule("throttle_join_max", 60),
			auditbot.EventUserPart: rule("throttle_part_max", 60),
			auditbot.EventUserQuit: rule("throttle_quit_max", 60),
			auditbot.EventUserNick: rule("throttle_nick_max", 60),
			auditbot.EventUserKick: rule("throttle_kick_max", 0),
		},
	}
}
