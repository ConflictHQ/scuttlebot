package registry

import (
	"fmt"
	"strings"
)

// EngagementConfig is the rules-of-engagement configuration for a registered agent.
// Passed to Register() at registration time; signed into the payload returned to the agent.
type EngagementConfig struct {
	// Channels is the list of IRC channels the agent should join.
	Channels []string `json:"channels,omitempty"`

	// OpsChannels is a subset of Channels where the agent is granted +o (operator).
	// Only meaningful for orchestrator-type agents.
	OpsChannels []string `json:"ops_channels,omitempty"`

	// Permissions is the list of allowed action types (e.g. "task.create").
	// Empty means no explicit restrictions.
	Permissions []string `json:"permissions,omitempty"`

	// RateLimit controls message throughput for this agent.
	RateLimit RateLimitConfig `json:"rate_limit,omitempty"`

	// Rules defines engagement behaviour rules for this agent.
	Rules EngagementRules `json:"engagement,omitempty"`

	// SystemPrompt is the per-agent system prompt the relay (or shepherd-
	// driven coordinator) can pass into the underlying LLM provider. Empty
	// = use the policy-level OnJoinDefault as a soft fallback. See #176.
	SystemPrompt string `json:"system_prompt,omitempty"`

	// Model overrides the LLM provider's default model for this agent.
	// Empty = use the provider's default.
	Model string `json:"model,omitempty"`

	// Temperature is an optional sampling override. nil/zero = provider default.
	// Stored as *float64 so a deliberate 0.0 is distinguishable from "unset".
	Temperature *float64 `json:"temperature,omitempty"`

	// ToolAllowlist names tools the agent is permitted to invoke. Empty = no
	// restriction. Honoured by relay binaries that gate tool dispatch.
	ToolAllowlist []string `json:"tool_allowlist,omitempty"`
}

// RateLimitConfig controls message throughput.
type RateLimitConfig struct {
	// MessagesPerSecond is the sustained send rate allowed. 0 means no limit.
	MessagesPerSecond float64 `json:"messages_per_second,omitempty"`

	// Burst is the maximum burst above MessagesPerSecond. 0 means no burst.
	Burst int `json:"burst,omitempty"`
}

// EngagementRules defines what message types and peers the agent should engage with.
type EngagementRules struct {
	// RespondToTypes restricts which message types trigger handler callbacks.
	// Empty means respond to all types.
	RespondToTypes []string `json:"respond_to_types,omitempty"`

	// IgnoreNicks is a list of IRC nicks whose messages are always ignored.
	IgnoreNicks []string `json:"ignore_nicks,omitempty"`
}

// Validate checks the EngagementConfig for obvious errors.
// Returns a descriptive error for the first problem found.
func (c EngagementConfig) Validate() error {
	for _, ch := range c.Channels {
		if !strings.HasPrefix(ch, "#") {
			return fmt.Errorf("engagement: channel %q must start with #", ch)
		}
		if strings.ContainsAny(ch, " \t\r\n,") {
			return fmt.Errorf("engagement: channel %q contains invalid characters", ch)
		}
		if len(ch) < 2 {
			return fmt.Errorf("engagement: channel %q is too short", ch)
		}
	}

	// OpsChannels must be a subset of Channels.
	joinSet := make(map[string]struct{}, len(c.Channels))
	for _, ch := range c.Channels {
		joinSet[ch] = struct{}{}
	}
	for _, ch := range c.OpsChannels {
		if _, ok := joinSet[ch]; !ok {
			return fmt.Errorf("engagement: ops_channel %q is not in channels list", ch)
		}
	}

	if c.RateLimit.MessagesPerSecond < 0 {
		return fmt.Errorf("engagement: rate_limit.messages_per_second must be >= 0")
	}
	if c.RateLimit.Burst < 0 {
		return fmt.Errorf("engagement: rate_limit.burst must be >= 0")
	}

	for _, t := range c.Rules.RespondToTypes {
		if t == "" {
			return fmt.Errorf("engagement: respond_to_types contains empty string")
		}
	}

	return nil
}
