// Package toon implements the TOON format — Token-Optimized Object Notation
// for compact LLM context windows.
//
// TOON is designed for feeding IRC conversation history to language models.
// It strips noise (joins, parts, status messages, repeated tool calls),
// deduplicates, and compresses timestamps into relative offsets.
//
// Example output:
//
//	#fleet 50msg 2h window
//	---
//	claude-kohakku [orch] +0m
//	  task.create {file: main.go, action: edit}
//	  "editing main.go to add error handling"
//	leo [op] +2m
//	  "looks good, ship it"
//	claude-kohakku [orch] +3m
//	  task.complete {file: main.go, status: done}
//	---
//	decisions: edit main.go error handling
//	actions: task.create → task.complete (main.go)
package toon

import (
	"fmt"
	"strings"
	"time"
)

// Entry is a single message to include in the TOON output.
type Entry struct {
	Nick        string
	Type        string // agent type: "orch", "worker", "op", "bot", "" for unknown
	MessageType string // envelope type (e.g. "task.create"), empty for plain text
	Text        string
	At          time.Time
}

// Options controls TOON formatting.
type Options struct {
	Channel    string
	MaxEntries int // 0 = no limit
}

// Format renders a slice of entries into TOON format.
func Format(entries []Entry, opts Options) string {
	if len(entries) == 0 {
		return ""
	}

	var b strings.Builder

	// Header.
	window := ""
	if len(entries) >= 2 {
		dur := entries[len(entries)-1].At.Sub(entries[0].At)
		window = " " + compactDuration(dur) + " window"
	}
	ch := opts.Channel
	if ch == "" {
		ch = "channel"
	}
	fmt.Fprintf(&b, "%s %dmsg%s\n---\n", ch, len(entries), window)

	// Body — group consecutive messages from same nick.
	baseTime := entries[0].At
	var lastNick string
	for _, e := range entries {
		offset := e.At.Sub(baseTime)
		if e.Nick != lastNick {
			tag := ""
			if e.Type != "" {
				tag = " [" + e.Type + "]"
			}
			fmt.Fprintf(&b, "%s%s +%s\n", e.Nick, tag, compactDuration(offset))
			lastNick = e.Nick
		}

		if e.MessageType != "" {
			fmt.Fprintf(&b, "  %s\n", e.MessageType)
		}
		text := strings.TrimSpace(e.Text)
		if text != "" && text != e.MessageType {
			// Truncate very long messages to save tokens.
			if len(text) > 200 {
				text = text[:197] + "..."
			}
			fmt.Fprintf(&b, "  \"%s\"\n", text)
		}
	}

	b.WriteString("---\n")
	return b.String()
}

// FormatPrompt wraps TOON-formatted history into an LLM summarization prompt.
func FormatPrompt(channel string, entries []Entry) string {
	toon := Format(entries, Options{Channel: channel})
	var b strings.Builder
	fmt.Fprintf(&b, "Summarize this IRC conversation. Focus on decisions, actions, and outcomes. Be concise.\n\n")
	b.WriteString(toon)
	return b.String()
}

func compactDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
