package auditbot_test

import (
	"sync"
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/internal/bots/auditbot"
)

func newBot(auditTypes ...string) (*auditbot.Bot, *auditbot.MemoryStore) {
	s := &auditbot.MemoryStore{}
	b := auditbot.New("localhost:6667", "pass", []string{"#fleet"}, auditTypes, s, nil)
	return b, s
}

func TestBotNameAndNew(t *testing.T) {
	b, _ := newBot()
	if b.Name() != "auditbot" {
		t.Errorf("Name(): got %q, want auditbot", b.Name())
	}
}

func TestRecordRegistryEvent(t *testing.T) {
	b, s := newBot("agent.registered")
	b.Record("agent-01", "agent.registered", "new registration")

	entries := s.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Kind != auditbot.KindRegistry {
		t.Errorf("Kind: got %q, want registry", e.Kind)
	}
	if e.Nick != "agent-01" {
		t.Errorf("Nick: got %q", e.Nick)
	}
	if e.MessageType != "agent.registered" {
		t.Errorf("MessageType: got %q", e.MessageType)
	}
	if e.Detail != "new registration" {
		t.Errorf("Detail: got %q", e.Detail)
	}
}

func TestRecordMultipleRegistryEvents(t *testing.T) {
	b, s := newBot()
	b.Record("agent-01", "agent.registered", "")
	b.Record("agent-01", "credentials.rotated", "")
	b.Record("agent-02", "agent.revoked", "policy violation")

	entries := s.All()
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
}

func TestStoreIsAppendOnly(t *testing.T) {
	s := &auditbot.MemoryStore{}
	if err := s.Append(auditbot.Entry{Nick: "a", MessageType: "task.create"}); err != nil {
		t.Fatalf("append a: %v", err)
	}
	if err := s.Append(auditbot.Entry{Nick: "b", MessageType: "task.complete"}); err != nil {
		t.Fatalf("append b: %v", err)
	}

	entries := s.All()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// Modifying the snapshot should not affect the store.
	entries[0].Nick = "tampered"
	fresh := s.All()
	if fresh[0].Nick == "tampered" {
		t.Error("store should be immutable — snapshot modification should not affect store")
	}
}

func TestAuditTypeFilter(t *testing.T) {
	// Only task.create should be audited via the IRC envelope filter, but
	// Record() always writes (registry events bypass the filter).
	b, s := newBot("task.create")
	b.Record("agent-01", "task.create", "")
	b.Record("agent-01", "task.update", "") // registry events always written

	entries := s.All()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries from Record(), got %d", len(entries))
	}
}

func TestAuditAllWhenNoFilter(t *testing.T) {
	b, s := newBot() // no filter = audit everything
	b.Record("a", "task.create", "")
	b.Record("b", "task.update", "")
	b.Record("c", "agent.hello", "")

	if got := len(s.All()); got != 3 {
		t.Errorf("expected 3 entries, got %d", got)
	}
}

func TestEntryTimestamp(t *testing.T) {
	b, s := newBot()
	b.Record("agent-01", "agent.registered", "")

	entries := s.All()
	if entries[0].At.IsZero() {
		t.Error("entry timestamp should not be zero")
	}
}

// --- Presence handler tests --------------------------------------------------

// disableThrottle clears the default throttle so presence handler tests can
// drive arbitrary volumes without tripping the rate limit.
func disableThrottle(b *auditbot.Bot) {
	b.SetThrottle(auditbot.ThrottleConfig{})
}

func TestHandleJoinAndPart(t *testing.T) {
	b, s := newBot()
	disableThrottle(b)

	auditbot.HandleJoinForTest(b, "#ops", "alice")
	auditbot.HandlePartForTest(b, "#ops", "alice")

	entries := s.All()
	if len(entries) != 2 {
		t.Fatalf("expected 2 presence entries, got %d", len(entries))
	}
	if entries[0].MessageType != auditbot.EventUserJoin || entries[0].Nick != "alice" || entries[0].Channel != "#ops" {
		t.Errorf("join entry wrong: %+v", entries[0])
	}
	if entries[1].MessageType != auditbot.EventUserPart || entries[1].Channel != "#ops" {
		t.Errorf("part entry wrong: %+v", entries[1])
	}
}

func TestHandleQuitFanOutsAcrossChannels(t *testing.T) {
	b, s := newBot()
	disableThrottle(b)

	auditbot.HandleQuitForTest(b, "alice", []string{"#ops", "#fleet", "#alpha"})

	entries := s.All()
	if len(entries) != 3 {
		t.Fatalf("expected 3 quit entries, got %d", len(entries))
	}
	for i, ch := range []string{"#ops", "#fleet", "#alpha"} {
		if entries[i].MessageType != auditbot.EventUserQuit {
			t.Errorf("entry %d: type %q, want user.quit", i, entries[i].MessageType)
		}
		if entries[i].Channel != ch {
			t.Errorf("entry %d: channel %q, want %q", i, entries[i].Channel, ch)
		}
		if entries[i].Nick != "alice" {
			t.Errorf("entry %d: nick %q", i, entries[i].Nick)
		}
	}
}

func TestHandleQuitFallbackEmptyChannel(t *testing.T) {
	b, s := newBot()
	disableThrottle(b)

	auditbot.HandleQuitForTest(b, "ghost", nil)

	entries := s.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 fallback quit entry, got %d", len(entries))
	}
	if entries[0].Channel != "" {
		t.Errorf("fallback channel should be empty, got %q", entries[0].Channel)
	}
	if entries[0].Nick != "ghost" {
		t.Errorf("nick: %q", entries[0].Nick)
	}
}

func TestHandleKickCapturesKickerAndReason(t *testing.T) {
	b, s := newBot()
	disableThrottle(b)

	auditbot.HandleKickForTest(b, "#ops", "alice", "steward", "spam")

	entries := s.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 kick entry, got %d", len(entries))
	}
	e := entries[0]
	if e.MessageType != auditbot.EventUserKick {
		t.Errorf("type: %q", e.MessageType)
	}
	if e.Nick != "alice" {
		t.Errorf("kicked nick: %q", e.Nick)
	}
	if e.Channel != "#ops" {
		t.Errorf("channel: %q", e.Channel)
	}
	if e.Detail != "by=steward reason=spam" {
		t.Errorf("detail: %q", e.Detail)
	}
}

func TestHandleKickWithoutReason(t *testing.T) {
	b, s := newBot()
	disableThrottle(b)

	auditbot.HandleKickForTest(b, "#ops", "alice", "steward", "")

	entries := s.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry")
	}
	if entries[0].Detail != "by=steward" {
		t.Errorf("detail: %q", entries[0].Detail)
	}
}

func TestHandleNickFanOutsAndCarriesRename(t *testing.T) {
	b, s := newBot()
	disableThrottle(b)

	auditbot.HandleNickForTest(b, "alice", "alice2", []string{"#ops", "#fleet"})

	entries := s.All()
	if len(entries) != 2 {
		t.Fatalf("expected 2 nick entries, got %d", len(entries))
	}
	for i, ch := range []string{"#ops", "#fleet"} {
		if entries[i].MessageType != auditbot.EventUserNick {
			t.Errorf("entry %d type %q", i, entries[i].MessageType)
		}
		if entries[i].Channel != ch {
			t.Errorf("entry %d channel %q", i, entries[i].Channel)
		}
		if entries[i].Nick != "alice2" {
			t.Errorf("entry %d nick %q (want new nick)", i, entries[i].Nick)
		}
		if entries[i].Detail != "old=alice new=alice2" {
			t.Errorf("entry %d detail %q", i, entries[i].Detail)
		}
	}
}

func TestHandleNickFallbackEmptyChannel(t *testing.T) {
	b, s := newBot()
	disableThrottle(b)

	auditbot.HandleNickForTest(b, "alice", "alice2", nil)

	entries := s.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry")
	}
	if entries[0].Channel != "" {
		t.Errorf("channel should be empty: %q", entries[0].Channel)
	}
	if entries[0].Detail != "old=alice new=alice2" {
		t.Errorf("detail: %q", entries[0].Detail)
	}
}

func TestPresenceFilterRespectsAuditTypes(t *testing.T) {
	// Configure an explicit allowlist that excludes user.part.
	b, s := newBot(auditbot.EventUserJoin)
	disableThrottle(b)

	auditbot.HandleJoinForTest(b, "#ops", "alice")
	auditbot.HandlePartForTest(b, "#ops", "alice")

	entries := s.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (only join allowed), got %d", len(entries))
	}
	if entries[0].MessageType != auditbot.EventUserJoin {
		t.Errorf("type: %q", entries[0].MessageType)
	}
}

// --- Throttle tests ----------------------------------------------------------

func TestThrottleAdmitsUpToMaxThenDrops(t *testing.T) {
	b, s := newBot()
	clk := newClock(time.Unix(0, 0))
	auditbot.SetClockForTest(b, clk.now)

	b.SetThrottle(auditbot.ThrottleConfig{
		PerType: map[string]auditbot.ThrottleRule{
			auditbot.EventUserJoin: {Max: 3, Window: time.Minute},
		},
	})

	// 3 admitted, 2 dropped.
	for i := 0; i < 5; i++ {
		auditbot.HandleJoinForTest(b, "#ops", "spammer")
	}

	entries := s.All()
	if len(entries) != 3 {
		t.Fatalf("expected 3 admitted entries, got %d", len(entries))
	}
	for _, e := range entries {
		if e.MessageType != auditbot.EventUserJoin {
			t.Errorf("unexpected type in initial window: %q", e.MessageType)
		}
	}
}

func TestThrottleEmitsSummaryOnWindowRollover(t *testing.T) {
	b, s := newBot()
	clk := newClock(time.Unix(0, 0))
	auditbot.SetClockForTest(b, clk.now)

	b.SetThrottle(auditbot.ThrottleConfig{
		PerType: map[string]auditbot.ThrottleRule{
			auditbot.EventUserJoin: {Max: 2, Window: time.Minute},
		},
	})

	// First window: 2 admitted, 3 dropped.
	for i := 0; i < 5; i++ {
		auditbot.HandleJoinForTest(b, "#ops", "spammer")
	}

	// Roll the clock past the window.
	clk.advance(2 * time.Minute)

	// Next event triggers rollover and emits summary, then is admitted.
	auditbot.HandleJoinForTest(b, "#ops", "spammer")

	entries := s.All()
	// 2 admitted + 1 summary + 1 admitted = 4
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d: %+v", len(entries), entries)
	}

	summary := entries[2]
	if summary.MessageType != auditbot.EventAuditThrottled {
		t.Errorf("expected throttle summary at index 2, got type %q", summary.MessageType)
	}
	if summary.Kind != auditbot.KindSystem {
		t.Errorf("summary kind: %q want system", summary.Kind)
	}
	wantDetail := "type=user.join dropped=3 window=1m0s"
	if summary.Detail != wantDetail {
		t.Errorf("summary detail: %q want %q", summary.Detail, wantDetail)
	}

	last := entries[3]
	if last.MessageType != auditbot.EventUserJoin {
		t.Errorf("last entry should be a fresh join; got %q", last.MessageType)
	}
}

func TestThrottleZeroMaxIsUncapped(t *testing.T) {
	b, s := newBot()
	clk := newClock(time.Unix(0, 0))
	auditbot.SetClockForTest(b, clk.now)

	b.SetThrottle(auditbot.ThrottleConfig{
		PerType: map[string]auditbot.ThrottleRule{
			auditbot.EventUserKick: {Max: 0, Window: time.Minute},
		},
	})

	for i := 0; i < 100; i++ {
		auditbot.HandleKickForTest(b, "#ops", "victim", "steward", "")
	}

	if got := len(s.All()); got != 100 {
		t.Errorf("expected 100 uncapped kick entries, got %d", got)
	}
}

func TestThrottleIsPerEventType(t *testing.T) {
	b, s := newBot()
	clk := newClock(time.Unix(0, 0))
	auditbot.SetClockForTest(b, clk.now)

	b.SetThrottle(auditbot.ThrottleConfig{
		PerType: map[string]auditbot.ThrottleRule{
			auditbot.EventUserJoin: {Max: 1, Window: time.Minute},
			auditbot.EventUserPart: {Max: 1, Window: time.Minute},
		},
	})

	auditbot.HandleJoinForTest(b, "#ops", "alice")
	auditbot.HandleJoinForTest(b, "#ops", "bob") // dropped
	auditbot.HandlePartForTest(b, "#ops", "alice")
	auditbot.HandlePartForTest(b, "#ops", "bob") // dropped

	entries := s.All()
	if len(entries) != 2 {
		t.Fatalf("expected 2 admitted entries (one per type), got %d", len(entries))
	}
	if entries[0].MessageType != auditbot.EventUserJoin {
		t.Errorf("entry 0: %q", entries[0].MessageType)
	}
	if entries[1].MessageType != auditbot.EventUserPart {
		t.Errorf("entry 1: %q", entries[1].MessageType)
	}
}

func TestDefaultThrottleConfigCapsPresenceUncapsKick(t *testing.T) {
	cfg := auditbot.DefaultThrottleConfig()

	for _, ty := range []string{auditbot.EventUserJoin, auditbot.EventUserPart, auditbot.EventUserQuit, auditbot.EventUserNick} {
		r, ok := cfg.PerType[ty]
		if !ok {
			t.Errorf("default throttle missing rule for %q", ty)
			continue
		}
		if r.Max <= 0 {
			t.Errorf("default %q max should be capped, got %d", ty, r.Max)
		}
		if r.Window <= 0 {
			t.Errorf("default %q window should be positive, got %s", ty, r.Window)
		}
	}
	if _, ok := cfg.PerType[auditbot.EventUserKick]; ok {
		t.Errorf("default throttle should not include user.kick (uncapped)")
	}
}

// clock is a deterministic time source for throttle tests.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(start time.Time) *clock { return &clock{t: start} }
func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
