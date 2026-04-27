package shepherd

import "context"

// Test-only accessors. Compiled only into the test binary.

// HandleGoalForTest exposes handleGoal.
func HandleGoalForTest(b *Bot, channel, nick, desc string) string {
	return b.handleGoal(channel, nick, desc)
}

// HandleListGoalsForTest exposes handleListGoals.
func HandleListGoalsForTest(b *Bot, channel string) string {
	return b.handleListGoals(channel)
}

// HandleDoneForTest exposes handleDone.
func HandleDoneForTest(b *Bot, channel, goalID string) string {
	return b.handleDone(channel, goalID)
}

// HandleAssignForTest exposes handleAssign.
func HandleAssignForTest(b *Bot, channel, args string) string {
	return b.handleAssign(channel, args)
}

// HandleStatusForTest exposes handleStatus.
func HandleStatusForTest(b *Bot, ctx context.Context, channel string) string {
	return b.handleStatus(ctx, channel)
}

// HandlePlanForTest exposes handlePlan.
func HandlePlanForTest(b *Bot, ctx context.Context, channel string) string {
	return b.handlePlan(ctx, channel)
}

// SaveStateForTest exposes saveState (caller is expected to hold no lock).
func SaveStateForTest(b *Bot) {
	b.mu.Lock()
	b.saveState()
	b.mu.Unlock()
}

// LoadStateForTest re-runs loadState.
func LoadStateForTest(b *Bot) { b.loadState() }

// SplitHostPortForTest exposes splitHostPort.
func SplitHostPortForTest(addr string) (string, int, error) {
	return splitHostPort(addr)
}

// GoalsFor returns a copy of the goal slice for a channel (for assertions).
func GoalsFor(b *Bot, channel string) []Goal {
	b.mu.Lock()
	defer b.mu.Unlock()
	src := b.goals[channel]
	out := make([]Goal, len(src))
	copy(out, src)
	return out
}

// AssignmentFor returns the assignment for a nick (for assertions).
func AssignmentFor(b *Bot, nick string) (Assignment, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a, ok := b.assignments[nick]
	if !ok || a == nil {
		return Assignment{}, false
	}
	return *a, true
}

// AppendHistory adds entries to the per-channel history buffer (for testing
// the LLM-fallback path of handleStatus).
func AppendHistory(b *Bot, channel string, lines ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.history[channel] = append(b.history[channel], lines...)
}
