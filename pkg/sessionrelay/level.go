package sessionrelay

import (
	"fmt"
	"strings"
)

// Level categorises a relay message by verbosity.
type Level int

const (
	LevelHeartbeat Level = iota + 1 // presence pings
	LevelLifecycle                  // connect/disconnect
	LevelAction                     // tool calls, results
	LevelContent                    // assistant text
	LevelReasoning                  // thinking blocks
)

// Resolution is a verbosity threshold for a channel.
// A channel receives messages with Level <= its Resolution.
type Resolution int

const (
	ResMinimal Resolution = Resolution(LevelLifecycle) // heartbeat + lifecycle
	ResActions Resolution = Resolution(LevelAction)    // + tool calls
	ResFull    Resolution = Resolution(LevelContent)   // + content text
	ResDebug   Resolution = Resolution(LevelReasoning) // + reasoning
)

// Accepts returns true if this resolution includes the given message level.
func (r Resolution) Accepts(l Level) bool {
	return Level(r) >= l
}

// ParseResolution parses a resolution name.
func ParseResolution(s string) (Resolution, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "minimal":
		return ResMinimal, nil
	case "actions":
		return ResActions, nil
	case "full":
		return ResFull, nil
	case "debug":
		return ResDebug, nil
	default:
		return 0, fmt.Errorf("sessionrelay: unknown resolution %q (use minimal, actions, full, debug)", s)
	}
}

func (r Resolution) String() string {
	switch r {
	case ResMinimal:
		return "minimal"
	case ResActions:
		return "actions"
	case ResFull:
		return "full"
	case ResDebug:
		return "debug"
	default:
		return fmt.Sprintf("Resolution(%d)", int(r))
	}
}
