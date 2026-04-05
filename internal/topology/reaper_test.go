package topology

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/internal/config"
)

// reapDry runs the reaper's expiry check without calling ChanServ.
// It returns the names of channels that would be reaped.
func reapDry(m *Manager, now time.Time) []string {
	if m.policy == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, rec := range m.channels {
		ttl := m.policy.TTLFor(rec.name)
		if ttl > 0 && m.policy.IsEphemeral(rec.name) && now.Sub(rec.provisionedAt) > ttl {
			out = append(out, rec.name)
		}
	}
	return out
}

func TestReaperExpiry(t *testing.T) {
	pol := NewPolicy(config.TopologyConfig{
		Types: []config.ChannelTypeConfig{
			{
				Name:      "task",
				Prefix:    "task.",
				Ephemeral: true,
				TTL:       config.Duration{Duration: 72 * time.Hour},
			},
			{
				Name:   "sprint",
				Prefix: "sprint.",
			},
		},
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := NewManager("localhost:6667", "topology", "pass", "", pol, log)

	// Simulate that channels were provisioned at different times.
	m.mu.Lock()
	m.channels["#task.old"] = channelRecord{name: "#task.old", provisionedAt: time.Now().Add(-80 * time.Hour)}
	m.channels["#task.fresh"] = channelRecord{name: "#task.fresh", provisionedAt: time.Now().Add(-10 * time.Hour)}
	m.channels["#sprint.2026-q2"] = channelRecord{name: "#sprint.2026-q2", provisionedAt: time.Now().Add(-200 * time.Hour)}
	m.mu.Unlock()

	expired := reapDry(m, time.Now())
	if len(expired) != 1 || expired[0] != "#task.old" {
		t.Errorf("expected [#task.old] to be expired, got %v", expired)
	}
}
