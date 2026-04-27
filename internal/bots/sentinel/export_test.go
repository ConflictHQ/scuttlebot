package sentinel

import (
	"context"
	"time"
)

// Test-only accessors. Compiled only into the test binary.

// ParseIncidentLineForTest exposes parseIncidentLine for tests.
func ParseIncidentLineForTest(line string) (nick, severity, reason string) {
	return parseIncidentLine(line)
}

// SeverityMeetsMinForTest exposes severityMeetsMin for tests.
func SeverityMeetsMinForTest(b *Bot, severity string) bool {
	return b.severityMeetsMin(severity)
}

// TestMsg is a public mirror of the internal msgEntry for prompt-building tests.
type TestMsg struct {
	At   time.Time
	Nick string
	Text string
}

// BuildPromptForTest exposes buildPrompt for tests with a synthesised window.
func BuildPromptForTest(b *Bot, channel string, msgs []TestMsg) string {
	conv := make([]msgEntry, 0, len(msgs))
	for _, m := range msgs {
		conv = append(conv, msgEntry{at: m.At, nick: m.Nick, text: m.Text})
	}
	return b.buildPrompt(channel, conv)
}

// SetDefaultsForTest exposes the unexported setDefaults.
func SetDefaultsForTest(c *Config) { c.setDefaults() }

// PruneTimeMapForTest exposes pruneTimeMap.
func PruneTimeMapForTest(m map[string]time.Time, maxAge time.Duration) {
	pruneTimeMap(m, maxAge)
}

// SplitHostPortForTest exposes splitHostPort.
func SplitHostPortForTest(addr string) (string, int, error) {
	return splitHostPort(addr)
}

// BufferForTest exposes buffer().
func BufferForTest(b *Bot, ctx context.Context, channel, nick, text string) {
	b.buffer(ctx, channel, nick, text)
}

// BufferLen returns the number of pending (un-analysed) messages buffered for
// a channel.
func BufferLen(b *Bot, channel string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	buf, ok := b.buffers[channel]
	if !ok {
		return 0
	}
	return len(buf.msgs)
}

// PushBuffer appends a message directly to the channel's buffer without
// triggering an analyse goroutine. The buffer's lastScan is initialised to
// the same instant as `at`, so the buffer is considered fresh until callers
// AgeBuffer it. Lets tests exercise flushStale deterministically.
func PushBuffer(b *Bot, channel, nick, text string, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	buf, ok := b.buffers[channel]
	if !ok {
		buf = &chanBuffer{lastScan: at}
		b.buffers[channel] = buf
	}
	buf.msgs = append(buf.msgs, msgEntry{at: at, nick: nick, text: text})
}

// AgeBuffer back-dates the lastScan of a channel's buffer so flushStale will
// consider it stale.
func AgeBuffer(b *Bot, channel string, age time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if buf, ok := b.buffers[channel]; ok {
		buf.lastScan = time.Now().Add(-age)
	}
}

// FlushStaleForTest exposes flushStale.
func FlushStaleForTest(b *Bot, ctx context.Context) {
	b.flushStale(ctx)
}

// AnalyseForTest synchronously runs analyse with a window of messages.
func AnalyseForTest(b *Bot, ctx context.Context, channel string, msgs []TestMsg) {
	conv := make([]msgEntry, 0, len(msgs))
	for _, m := range msgs {
		conv = append(conv, msgEntry{at: m.At, nick: m.Nick, text: m.Text})
	}
	b.analyse(ctx, channel, conv)
}

// CooldownLen returns the size of the cooldown map (used for assertions).
func CooldownLen(b *Bot) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.cooldown)
}

// ParseAndReportForTest exposes parseAndReport (no-op without a client).
func ParseAndReportForTest(b *Bot, channel, result string) {
	b.parseAndReport(channel, result)
}
