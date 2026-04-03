package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/conflicthq/scuttlebot/internal/store"
)

// BehaviorConfig defines a pre-registered system bot behavior.
type BehaviorConfig struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Nick             string   `json:"nick"`
	Enabled          bool     `json:"enabled"`
	JoinAllChannels  bool     `json:"join_all_channels"`
	ExcludeChannels  []string `json:"exclude_channels"`
	RequiredChannels []string `json:"required_channels"`
	// Config holds bot-specific configuration. The schema is defined per bot
	// in the UI; the backend stores and returns it opaquely.
	Config map[string]any `json:"config,omitempty"`
}

// AgentPolicy defines requirements applied to all registering agents.
type AgentPolicy struct {
	RequireCheckin   bool     `json:"require_checkin"`
	CheckinChannel   string   `json:"checkin_channel"`
	RequiredChannels []string `json:"required_channels"`
}

// LoggingPolicy configures message logging.
type LoggingPolicy struct {
	Enabled    bool   `json:"enabled"`
	Dir        string `json:"dir"`          // directory to write log files into
	Format     string `json:"format"`       // "jsonl" | "csv" | "text"
	Rotation   string `json:"rotation"`     // "none" | "daily" | "weekly" | "size"
	MaxSizeMB  int    `json:"max_size_mb"`  // size rotation threshold (MiB); 0 = unlimited
	PerChannel bool   `json:"per_channel"`  // separate file per channel
	MaxAgeDays int    `json:"max_age_days"` // prune rotated files older than N days; 0 = keep all
}

// BridgePolicy configures bridge-specific UI/relay behavior.
type BridgePolicy struct {
	// WebUserTTLMinutes controls how long HTTP bridge sender nicks remain
	// visible in the channel user list after their last post.
	WebUserTTLMinutes int `json:"web_user_ttl_minutes"`
}

// PolicyLLMBackend stores an LLM backend configuration in the policy store.
// This allows backends to be added and edited from the web UI rather than
// requiring a change to scuttlebot.yaml.
//
// API keys are write-only — GET responses replace them with "***" when set.
type PolicyLLMBackend struct {
	Name         string   `json:"name"`
	Backend      string   `json:"backend"`
	APIKey       string   `json:"api_key,omitempty"`
	BaseURL      string   `json:"base_url,omitempty"`
	Model        string   `json:"model,omitempty"`
	Region       string   `json:"region,omitempty"`
	AWSKeyID     string   `json:"aws_key_id,omitempty"`
	AWSSecretKey string   `json:"aws_secret_key,omitempty"`
	Allow        []string `json:"allow,omitempty"`
	Block        []string `json:"block,omitempty"`
	Default      bool     `json:"default,omitempty"`
}

// Policies is the full mutable settings blob, persisted to policies.json.
type Policies struct {
	Behaviors   []BehaviorConfig   `json:"behaviors"`
	AgentPolicy AgentPolicy        `json:"agent_policy"`
	Bridge      BridgePolicy       `json:"bridge"`
	Logging     LoggingPolicy      `json:"logging"`
	LLMBackends []PolicyLLMBackend `json:"llm_backends,omitempty"`
}

// defaultBehaviors lists every built-in bot with conservative defaults (disabled).
var defaultBehaviors = []BehaviorConfig{
	{
		ID:              "auditbot",
		Name:            "Auditor",
		Description:     "Immutable append-only audit trail of agent actions and credential lifecycle events.",
		Nick:            "auditbot",
		JoinAllChannels: true,
	},
	{
		ID:              "scribe",
		Name:            "Scribe",
		Description:     "Records all channel messages to a structured log store.",
		Nick:            "scribe",
		JoinAllChannels: true,
	},
	{
		ID:          "herald",
		Name:        "Herald",
		Description: "Routes event notifications from external systems to IRC channels.",
		Nick:        "herald",
	},
	{
		ID:              "oracle",
		Name:            "Oracle",
		Description:     "On-demand channel summarisation via DM using an LLM.",
		Nick:            "oracle",
		JoinAllChannels: true,
	},
	{
		ID:              "warden",
		Name:            "Warden",
		Description:     "Enforces channel moderation — detects floods and malformed messages, escalates warn → mute → kick.",
		Nick:            "warden",
		JoinAllChannels: true,
	},
	{
		ID:              "scroll",
		Name:            "Scroll",
		Description:     "Replays channel history to users via DM on request.",
		Nick:            "scroll",
		JoinAllChannels: true,
	},
	{
		ID:              "systembot",
		Name:            "Systembot",
		Description:     "Logs IRC system events (joins, parts, quits, mode changes) to a store.",
		Nick:            "systembot",
		JoinAllChannels: true,
	},
	{
		ID:              "snitch",
		Name:            "Snitch",
		Description:     "Watches for erratic behaviour and alerts operators via DM or a dedicated channel.",
		Nick:            "snitch",
		JoinAllChannels: true,
	},
	{
		ID:              "sentinel",
		Name:            "Sentinel",
		Description:     "LLM-powered channel observer. Detects policy violations and posts structured incident reports to a mod channel. Never takes enforcement action.",
		Nick:            "sentinel",
		JoinAllChannels: true,
	},
	{
		ID:              "steward",
		Name:            "Steward",
		Description:     "Acts on sentinel incident reports — issues warnings, mutes, or kicks based on severity. Operators can also issue direct commands via DM.",
		Nick:            "steward",
		JoinAllChannels: true,
	},
}

// PolicyStore persists Policies to a JSON file or database.
type PolicyStore struct {
	mu                      sync.RWMutex
	path                    string
	data                    Policies
	defaultBridgeTTLMinutes int
	onChange                func(Policies)
	db                      *store.Store // when non-nil, supersedes path
}

func NewPolicyStore(path string, defaultBridgeTTLMinutes int) (*PolicyStore, error) {
	if defaultBridgeTTLMinutes <= 0 {
		defaultBridgeTTLMinutes = 5
	}
	ps := &PolicyStore{
		path:                    path,
		defaultBridgeTTLMinutes: defaultBridgeTTLMinutes,
	}
	ps.data.Behaviors = defaultBehaviors
	ps.data.Bridge.WebUserTTLMinutes = defaultBridgeTTLMinutes
	if err := ps.load(); err != nil {
		return nil, err
	}
	return ps, nil
}

func (ps *PolicyStore) load() error {
	raw, err := os.ReadFile(ps.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("policies: read %s: %w", ps.path, err)
	}
	return ps.applyRaw(raw)
}

// SetStore switches the policy store to database-backed persistence. The
// current in-memory defaults are merged with any saved policies in the store.
func (ps *PolicyStore) SetStore(db *store.Store) error {
	raw, err := db.PolicyGet()
	if err != nil {
		return fmt.Errorf("policies: load from db: %w", err)
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.db = db
	if raw == nil {
		return nil // no saved policies yet; keep defaults
	}
	return ps.applyRaw(raw)
}

// applyRaw merges a JSON blob into the in-memory policy state.
// Caller must hold ps.mu if called after initialisation.
func (ps *PolicyStore) applyRaw(raw []byte) error {
	var p Policies
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("policies: parse: %w", err)
	}
	ps.normalize(&p)
	// Merge saved behaviors over defaults so new built-ins appear automatically.
	saved := make(map[string]BehaviorConfig, len(p.Behaviors))
	for _, b := range p.Behaviors {
		saved[b.ID] = b
	}
	for i, def := range ps.data.Behaviors {
		if sv, ok := saved[def.ID]; ok {
			ps.data.Behaviors[i] = sv
		}
	}
	ps.data.AgentPolicy = p.AgentPolicy
	ps.data.Bridge = p.Bridge
	ps.data.Logging = p.Logging
	ps.data.LLMBackends = p.LLMBackends
	return nil
}

func (ps *PolicyStore) save() error {
	raw, err := json.MarshalIndent(ps.data, "", "  ")
	if err != nil {
		return err
	}
	if ps.db != nil {
		return ps.db.PolicySet(raw)
	}
	return os.WriteFile(ps.path, raw, 0600)
}

func (ps *PolicyStore) Get() Policies {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.data
}

// OnChange registers a callback invoked (in a new goroutine) after each
// successful Set(). The callback receives the new Policies snapshot.
func (ps *PolicyStore) OnChange(fn func(Policies)) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.onChange = fn
}

func (ps *PolicyStore) Set(p Policies) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.normalize(&p)
	ps.data = p
	if err := ps.save(); err != nil {
		return err
	}
	if ps.onChange != nil {
		snap := ps.data
		fn := ps.onChange
		go fn(snap)
	}
	return nil
}

func (ps *PolicyStore) normalize(p *Policies) {
	if p.Bridge.WebUserTTLMinutes <= 0 {
		p.Bridge.WebUserTTLMinutes = ps.defaultBridgeTTLMinutes
	}
}

// --- HTTP handlers ---

func (s *Server) handleGetPolicies(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.policies.Get())
}

func (s *Server) handlePutPolicies(w http.ResponseWriter, r *http.Request) {
	var p Policies
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.policies.Set(p); err != nil {
		s.log.Error("save policies", "err", err)
		writeError(w, http.StatusInternalServerError, "save failed")
		return
	}
	writeJSON(w, http.StatusOK, s.policies.Get())
}
