// Package topology manages IRC channel provisioning.
//
// The Manager connects to Ergo as a privileged oper account and provisions
// channels via ChanServ: registration, topics, and access lists (ops/voice).
// Users define topology in scuttlebot config; this package creates and
// maintains it in Ergo.
package topology

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lrstanley/girc"
)

// ChannelConfig describes a channel to provision.
type ChannelConfig struct {
	// Name is the full channel name including the # prefix.
	// Convention: #fleet, #project.{name}, #project.{name}.{topic}
	Name string

	// Topic is the initial channel topic (shared state header).
	Topic string

	// Ops is a list of nicks to grant +o (channel operator) status.
	Ops []string

	// Voice is a list of nicks to grant +v status.
	Voice []string

	// Autojoin is a list of bot nicks to invite after provisioning.
	Autojoin []string
}

// channelRecord tracks a provisioned channel for TTL-based reaping.
type channelRecord struct {
	name          string
	provisionedAt time.Time
}

// Manager provisions and maintains IRC channel topology.
type Manager struct {
	ircAddr  string
	nick     string
	password string
	log      *slog.Logger
	policy   *Policy
	client   *girc.Client

	mu       sync.Mutex
	channels map[string]channelRecord // channel name → record
}

// NewManager creates a topology Manager. nick and password are the Ergo
// credentials of the scuttlebot oper account used to manage channels.
// policy may be nil if the caller only uses the manager for ad-hoc provisioning.
func NewManager(ircAddr, nick, password string, policy *Policy, log *slog.Logger) *Manager {
	return &Manager{
		ircAddr:  ircAddr,
		nick:     nick,
		password: password,
		policy:   policy,
		log:      log,
		channels: make(map[string]channelRecord),
	}
}

// Policy returns the policy attached to this manager, or nil.
func (m *Manager) Policy() *Policy { return m.policy }

// Connect establishes the IRC connection used for channel management.
// Call before Provision.
func (m *Manager) Connect(ctx context.Context) error {
	host, port, err := splitHostPort(m.ircAddr)
	if err != nil {
		return fmt.Errorf("topology: parse irc addr: %w", err)
	}

	c := girc.New(girc.Config{
		Server: host,
		Port:   port,
		Nick:   m.nick,
		User:   "scuttlebot",
		Name:   "scuttlebot topology manager",
		SASL:   &girc.SASLPlain{User: m.nick, Pass: m.password},
		SSL:    false,
	})

	connected := make(chan struct{})
	c.Handlers.AddBg(girc.CONNECTED, func(client *girc.Client, e girc.Event) {
		close(connected)
	})

	go func() {
		if err := c.Connect(); err != nil {
			m.log.Error("topology irc connection error", "err", err)
		}
	}()

	select {
	case <-connected:
		m.client = c
		return nil
	case <-ctx.Done():
		c.Close()
		return ctx.Err()
	case <-time.After(30 * time.Second):
		c.Close()
		return fmt.Errorf("topology: timed out connecting to IRC")
	}
}

// Close disconnects from IRC.
func (m *Manager) Close() {
	if m.client != nil {
		m.client.Close()
	}
}

// Provision creates and configures a set of channels. It is idempotent —
// calling it multiple times with the same config is safe.
func (m *Manager) Provision(channels []ChannelConfig) error {
	if m.client == nil {
		return fmt.Errorf("topology: not connected — call Connect first")
	}
	for _, ch := range channels {
		if err := ValidateName(ch.Name); err != nil {
			return err
		}
		if err := m.provision(ch); err != nil {
			return err
		}
	}
	return nil
}

// SetTopic updates the topic on an existing channel.
func (m *Manager) SetTopic(channel, topic string) error {
	if m.client == nil {
		return fmt.Errorf("topology: not connected")
	}
	m.chanserv("TOPIC %s %s", channel, topic)
	return nil
}

// ProvisionEphemeral creates a short-lived task channel.
// Convention: #task.{id}
func (m *Manager) ProvisionEphemeral(id string) (string, error) {
	name := "#task." + id
	if err := ValidateName(name); err != nil {
		return "", err
	}
	if err := m.provision(ChannelConfig{Name: name}); err != nil {
		return "", err
	}
	return name, nil
}

// DestroyEphemeral drops an ephemeral task channel.
func (m *Manager) DestroyEphemeral(channel string) {
	m.chanserv("DROP %s", channel)
}

// ProvisionChannel provisions a single channel and invites its autojoin nicks.
// It applies the manager's Policy if set; the caller may override autojoin via
// the ChannelConfig directly.
func (m *Manager) ProvisionChannel(ch ChannelConfig) error {
	if err := ValidateName(ch.Name); err != nil {
		return err
	}
	if err := m.provision(ch); err != nil {
		return err
	}
	if len(ch.Autojoin) > 0 {
		m.Invite(ch.Name, ch.Autojoin)
	}
	return nil
}

// Invite sends IRC INVITE to each nick in nicks for the given channel.
// Invite is best-effort: nicks that are not connected are silently skipped.
func (m *Manager) Invite(channel string, nicks []string) {
	if m.client == nil {
		return
	}
	for _, nick := range nicks {
		m.client.Cmd.Invite(nick, channel)
	}
}

func (m *Manager) provision(ch ChannelConfig) error {
	// Register with ChanServ (idempotent — fails silently if already registered).
	m.chanserv("REGISTER %s", ch.Name)
	// Give ChanServ time to process the registration before issuing follow-up
	// commands. Retry the sleep up to 3 times so transient load doesn't cause
	// TOPIC/ACCESS commands to fire before registration completes.
	for range 3 {
		time.Sleep(200 * time.Millisecond)
		if m.client.IsConnected() {
			break
		}
	}

	if ch.Topic != "" {
		m.chanserv("TOPIC %s %s", ch.Name, ch.Topic)
	}

	for _, nick := range ch.Ops {
		m.chanserv("ACCESS %s ADD %s OP", ch.Name, nick)
	}
	for _, nick := range ch.Voice {
		m.chanserv("ACCESS %s ADD %s VOICE", ch.Name, nick)
	}

	if len(ch.Autojoin) > 0 {
		m.Invite(ch.Name, ch.Autojoin)
	}

	m.mu.Lock()
	m.channels[ch.Name] = channelRecord{name: ch.Name, provisionedAt: time.Now()}
	m.mu.Unlock()

	m.log.Info("provisioned channel", "channel", ch.Name)
	return nil
}

// DropChannel drops an IRC channel via ChanServ DROP and removes it from the
// channel registry. Use for ephemeral channels that have expired or been closed.
func (m *Manager) DropChannel(channel string) {
	m.chanserv("DROP %s", channel)
	m.mu.Lock()
	delete(m.channels, channel)
	m.mu.Unlock()
	m.log.Info("dropped channel", "channel", channel)
}

// StartReaper starts a background goroutine that drops ephemeral channels once
// their TTL has elapsed. The reaper runs until ctx is cancelled.
// Policy must be set on the Manager for TTL rules to be evaluated.
func (m *Manager) StartReaper(ctx context.Context) {
	if m.policy == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.reap()
			}
		}
	}()
}

func (m *Manager) reap() {
	now := time.Now()
	m.mu.Lock()
	expired := make([]channelRecord, 0)
	for _, rec := range m.channels {
		ttl := m.policy.TTLFor(rec.name)
		if ttl > 0 && m.policy.IsEphemeral(rec.name) && now.Sub(rec.provisionedAt) > ttl {
			expired = append(expired, rec)
		}
	}
	m.mu.Unlock()
	for _, rec := range expired {
		m.log.Info("reaping expired ephemeral channel", "channel", rec.name, "age", now.Sub(rec.provisionedAt).Round(time.Minute))
		m.DropChannel(rec.name)
	}
}

func (m *Manager) chanserv(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	m.client.Cmd.Message("ChanServ", msg)
}

// ValidateName checks that a channel name follows scuttlebot conventions.
func ValidateName(name string) error {
	if !strings.HasPrefix(name, "#") {
		return fmt.Errorf("topology: channel name must start with #: %q", name)
	}
	if len(name) < 2 {
		return fmt.Errorf("topology: channel name too short: %q", name)
	}
	if strings.Contains(name, " ") {
		return fmt.Errorf("topology: channel name must not contain spaces: %q", name)
	}
	return nil
}

func splitHostPort(addr string) (string, int, error) {
	parts := strings.SplitN(addr, ":", 2)
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid address %q (expected host:port)", addr)
	}
	var port int
	if _, err := fmt.Sscan(parts[1], &port); err != nil {
		return "", 0, fmt.Errorf("invalid port in %q: %w", addr, err)
	}
	return parts[0], port, nil
}
