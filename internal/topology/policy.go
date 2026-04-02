package topology

import (
	"context"
	"strings"
	"time"

	"github.com/conflicthq/scuttlebot/internal/config"
)

// ChannelType is the resolved policy for a class of channels.
type ChannelType struct {
	Name        string
	Prefix      string
	Autojoin    []string
	Supervision string
	Ephemeral   bool
	TTL         time.Duration
}

// EventType classifies a channel lifecycle event.
type EventType string

const (
	EventCreated     EventType = "created"
	EventClosed      EventType = "closed"
	EventReopened    EventType = "reopened"
	EventTransferred EventType = "transferred"
)

// ChannelEvent is emitted on channel lifecycle transitions.
type ChannelEvent struct {
	Type    EventType
	Channel string
	By      string            // nick that triggered the event
	Meta    map[string]string // optional domain-specific metadata
}

// EventHook is implemented by anything that reacts to channel lifecycle events.
type EventHook interface {
	OnChannelEvent(ctx context.Context, event ChannelEvent) error
}

// Policy is the domain-agnostic evaluation layer between topology config and
// the runtime (IRC, bot invites, API). It answers questions like:
//
//   - What type is #task.gh-42?
//   - Which bots should join #incident.p1?
//   - Where should summaries from #feature.auth surface?
//
// Rules come entirely from config — the Policy itself contains no hardcoded
// domain knowledge.
type Policy struct {
	staticChannels []config.StaticChannelConfig
	types          []ChannelType
}

// NewPolicy constructs a Policy from the topology section of the config.
func NewPolicy(cfg config.TopologyConfig) *Policy {
	types := make([]ChannelType, 0, len(cfg.Types))
	for _, t := range cfg.Types {
		types = append(types, ChannelType{
			Name:        t.Name,
			Prefix:      t.Prefix,
			Autojoin:    append([]string(nil), t.Autojoin...),
			Supervision: t.Supervision,
			Ephemeral:   t.Ephemeral,
			TTL:         t.TTL.Duration,
		})
	}
	return &Policy{
		staticChannels: append([]config.StaticChannelConfig(nil), cfg.Channels...),
		types:          types,
	}
}

// Match returns the ChannelType for the given channel name by prefix, or nil
// if no type matches. Channel names are matched after stripping the leading #.
func (p *Policy) Match(channel string) *ChannelType {
	slug := strings.TrimPrefix(channel, "#")
	for i := range p.types {
		if strings.HasPrefix(slug, p.types[i].Prefix) {
			return &p.types[i]
		}
	}
	return nil
}

// AutojoinFor returns the bot nicks that should join channel.
// For dynamic channels this comes from the matching ChannelType.
// For static channels it comes from the StaticChannelConfig.
// Returns nil if no rule matches.
func (p *Policy) AutojoinFor(channel string) []string {
	// Check static channels first (exact match).
	for _, sc := range p.staticChannels {
		if strings.EqualFold(sc.Name, channel) {
			return append([]string(nil), sc.Autojoin...)
		}
	}
	if t := p.Match(channel); t != nil {
		return append([]string(nil), t.Autojoin...)
	}
	return nil
}

// SupervisionFor returns the coordination/supervision channel for the given
// channel, or an empty string if none is configured for its type.
func (p *Policy) SupervisionFor(channel string) string {
	if t := p.Match(channel); t != nil {
		return t.Supervision
	}
	return ""
}

// TypeName returns the type name for the given channel, or "unknown".
func (p *Policy) TypeName(channel string) string {
	if t := p.Match(channel); t != nil {
		return t.Name
	}
	return ""
}

// IsEphemeral reports whether channels of the matched type are ephemeral.
func (p *Policy) IsEphemeral(channel string) bool {
	if t := p.Match(channel); t != nil {
		return t.Ephemeral
	}
	return false
}

// TTLFor returns the TTL for the matched channel type, or zero if none.
func (p *Policy) TTLFor(channel string) time.Duration {
	if t := p.Match(channel); t != nil {
		return t.TTL
	}
	return 0
}

// StaticChannels returns the list of channels to provision at startup.
func (p *Policy) StaticChannels() []config.StaticChannelConfig {
	return append([]config.StaticChannelConfig(nil), p.staticChannels...)
}

// Types returns all registered channel types.
func (p *Policy) Types() []ChannelType {
	return append([]ChannelType(nil), p.types...)
}
