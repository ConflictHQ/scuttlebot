// Internal tests for the nickWindow sliding-window logic.
// In package snitch (not snitch_test) to access unexported types.
package snitch

import (
	"testing"
	"time"
)

func TestNickWindowTrimRemovesOldMsgs(t *testing.T) {
	now := time.Now()
	nw := &nickWindow{
		msgs: []time.Time{
			now.Add(-10 * time.Second), // old — should be trimmed
			now.Add(-1 * time.Second),  // recent — should stay
		},
	}
	nw.trim(now, 5*time.Second, 30*time.Second)
	if len(nw.msgs) != 1 {
		t.Errorf("expected 1 msg after trim, got %d", len(nw.msgs))
	}
}

func TestNickWindowTrimKeepsAllRecent(t *testing.T) {
	now := time.Now()
	nw := &nickWindow{
		msgs: []time.Time{
			now.Add(-1 * time.Second),
			now.Add(-2 * time.Second),
			now.Add(-3 * time.Second),
		},
	}
	nw.trim(now, 10*time.Second, 30*time.Second)
	if len(nw.msgs) != 3 {
		t.Errorf("expected 3 msgs after trim, got %d", len(nw.msgs))
	}
}

func TestNickWindowTrimRemovesOldJoinParts(t *testing.T) {
	now := time.Now()
	nw := &nickWindow{
		joinPart: []time.Time{
			now.Add(-60 * time.Second), // too old
			now.Add(-5 * time.Second),  // recent
		},
	}
	nw.trim(now, 5*time.Second, 30*time.Second)
	if len(nw.joinPart) != 1 {
		t.Errorf("expected 1 join/part after trim, got %d", len(nw.joinPart))
	}
}

func TestNickWindowTrimEmptyNoop(t *testing.T) {
	nw := &nickWindow{}
	// Should not panic on empty slices.
	nw.trim(time.Now(), 5*time.Second, 30*time.Second)
	if len(nw.msgs) != 0 || len(nw.joinPart) != 0 {
		t.Error("expected empty after trimming empty window")
	}
}

func TestNickWindowTrimAllOld(t *testing.T) {
	now := time.Now()
	nw := &nickWindow{
		msgs: []time.Time{
			now.Add(-100 * time.Second),
			now.Add(-200 * time.Second),
		},
		joinPart: []time.Time{
			now.Add(-90 * time.Second),
		},
	}
	nw.trim(now, 5*time.Second, 30*time.Second)
	if len(nw.msgs) != 0 {
		t.Errorf("expected 0 msgs after trimming all-old, got %d", len(nw.msgs))
	}
	if len(nw.joinPart) != 0 {
		t.Errorf("expected 0 join/parts after trimming all-old, got %d", len(nw.joinPart))
	}
}

// Test the flood detection path at the Bot level. We reach into the Bot's
// internal window map by calling recordMsg directly, which is the same path
// a real PRIVMSG would trigger. This validates the counting logic without
// requiring an IRC connection.

func TestFloodDetectionCounting(t *testing.T) {
	cfg := Config{
		IRCAddr:       "127.0.0.1:6667",
		Nick:          "snitch",
		FloodMessages: 3,
		FloodWindow:   10 * time.Second,
	}
	cfg.setDefaults()

	b := &Bot{
		cfg:     cfg,
		windows: make(map[string]map[string]*nickWindow),
		alerted: make(map[string]time.Time),
	}

	// Record 2 messages — below threshold.
	b.recordMsg("#fleet", "spammer")
	b.recordMsg("#fleet", "spammer")
	w := b.window("#fleet", "spammer")
	if len(w.msgs) != 2 {
		t.Errorf("expected 2 msgs in window, got %d", len(w.msgs))
	}

	// Record a third — at threshold.
	b.recordMsg("#fleet", "spammer")
	w = b.window("#fleet", "spammer")
	if len(w.msgs) != 3 {
		t.Errorf("expected 3 msgs in window, got %d", len(w.msgs))
	}
}

func TestJoinPartCounting(t *testing.T) {
	cfg := Config{
		IRCAddr:           "127.0.0.1:6667",
		Nick:              "snitch",
		JoinPartThreshold: 3,
		JoinPartWindow:    30 * time.Second,
	}
	cfg.setDefaults()

	b := &Bot{
		cfg:     cfg,
		windows: make(map[string]map[string]*nickWindow),
		alerted: make(map[string]time.Time),
	}

	// 2 join/part events — below threshold.
	b.recordJoinPart("#fleet", "cycler")
	b.recordJoinPart("#fleet", "cycler")
	w := b.window("#fleet", "cycler")
	if len(w.joinPart) != 2 {
		t.Errorf("expected 2 join/parts before threshold, got %d", len(w.joinPart))
	}

	// 3rd event hits threshold — window is reset to nil after alert fires.
	b.recordJoinPart("#fleet", "cycler")
	w = b.window("#fleet", "cycler")
	if len(w.joinPart) != 0 {
		t.Errorf("expected joinPart reset to 0 after threshold hit, got %d", len(w.joinPart))
	}
}

func TestWindowIsolatedPerNick(t *testing.T) {
	cfg := Config{IRCAddr: "127.0.0.1:6667", FloodMessages: 5, FloodWindow: 10 * time.Second}
	cfg.setDefaults()
	b := &Bot{
		cfg:     cfg,
		windows: make(map[string]map[string]*nickWindow),
		alerted: make(map[string]time.Time),
	}

	b.recordMsg("#fleet", "alice")
	b.recordMsg("#fleet", "alice")
	b.recordMsg("#fleet", "bob")

	wa := b.window("#fleet", "alice")
	wb := b.window("#fleet", "bob")
	if len(wa.msgs) != 2 {
		t.Errorf("alice: expected 2, got %d", len(wa.msgs))
	}
	if len(wb.msgs) != 1 {
		t.Errorf("bob: expected 1, got %d", len(wb.msgs))
	}
}

func TestWindowIsolatedPerChannel(t *testing.T) {
	cfg := Config{IRCAddr: "127.0.0.1:6667"}
	cfg.setDefaults()
	b := &Bot{
		cfg:     cfg,
		windows: make(map[string]map[string]*nickWindow),
		alerted: make(map[string]time.Time),
	}

	b.recordMsg("#fleet", "alice")
	b.recordMsg("#ops", "alice")

	wf := b.window("#fleet", "alice")
	wo := b.window("#ops", "alice")
	if len(wf.msgs) != 1 || len(wo.msgs) != 1 {
		t.Errorf("expected 1 msg per channel, fleet=%d ops=%d", len(wf.msgs), len(wo.msgs))
	}
}
