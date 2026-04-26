package auditbot

import "time"

// Test-only accessors to drive the unexported handler logic without an IRC
// connection. These are compiled only into the test binary.

// HandleJoinForTest is the test-visible alias for handleJoin.
func HandleJoinForTest(b *Bot, channel, nick string) { b.handleJoin(channel, nick) }

// HandlePartForTest is the test-visible alias for handlePart.
func HandlePartForTest(b *Bot, channel, nick string) { b.handlePart(channel, nick) }

// HandleQuitForTest is the test-visible alias for handleQuit.
func HandleQuitForTest(b *Bot, nick string, channels []string) { b.handleQuit(nick, channels) }

// HandleKickForTest is the test-visible alias for handleKick.
func HandleKickForTest(b *Bot, channel, kicked, kicker, reason string) {
	b.handleKick(channel, kicked, kicker, reason)
}

// HandleNickForTest is the test-visible alias for handleNick.
func HandleNickForTest(b *Bot, oldNick, newNick string, channels []string) {
	b.handleNick(oldNick, newNick, channels)
}

// SetClockForTest overrides the bot's time source (used for deterministic
// throttle window rollovers in tests).
func SetClockForTest(b *Bot, now func() time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.now = now
}
