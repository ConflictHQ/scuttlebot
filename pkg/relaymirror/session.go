package relaymirror

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SessionWatcher watches a directory for new session files and calls onFile
// when one is discovered. Designed for Gemini CLI session discovery.
type SessionWatcher struct {
	dir     string
	prefix  string // e.g. "session-"
	timeout time.Duration
}

// NewSessionWatcher creates a watcher for session files matching prefix in dir.
func NewSessionWatcher(dir, prefix string, timeout time.Duration) *SessionWatcher {
	return &SessionWatcher{dir: dir, prefix: prefix, timeout: timeout}
}

// Discover waits for a new session file to appear in the directory.
// Returns the path of the discovered file.
func (w *SessionWatcher) Discover(ctx context.Context, existingFiles map[string]bool) (string, error) {
	deadline := time.After(w.timeout)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline:
			return "", fmt.Errorf("session discovery timed out after %s", w.timeout)
		case <-tick.C:
			entries, err := os.ReadDir(w.dir)
			if err != nil {
				continue
			}
			// Find newest file matching prefix that isn't pre-existing.
			var candidates []os.DirEntry
			for _, e := range entries {
				if e.IsDir() || !strings.HasPrefix(e.Name(), w.prefix) {
					continue
				}
				if existingFiles[e.Name()] {
					continue
				}
				candidates = append(candidates, e)
			}
			if len(candidates) == 0 {
				continue
			}
			// Sort by mod time, pick newest.
			sort.Slice(candidates, func(i, j int) bool {
				ii, _ := candidates[i].Info()
				jj, _ := candidates[j].Info()
				if ii == nil || jj == nil {
					return false
				}
				return ii.ModTime().After(jj.ModTime())
			})
			return filepath.Join(w.dir, candidates[0].Name()), nil
		}
	}
}

// SnapshotDir returns a set of filenames currently in dir.
func SnapshotDir(dir string) map[string]bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make(map[string]bool, len(entries))
	for _, e := range entries {
		out[e.Name()] = true
	}
	return out
}

// GeminiMessage is a message from a Gemini CLI session file.
type GeminiMessage struct {
	Type      string           `json:"type"` // "user", "gemini"
	Content   string           `json:"content,omitempty"`
	ToolCalls []GeminiToolCall `json:"toolCalls,omitempty"`
}

// GeminiToolCall is a tool call in a Gemini session.
type GeminiToolCall struct {
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`
	Result json.RawMessage `json:"result,omitempty"`
	Status string          `json:"status"`
}

// GeminiSession is the top-level structure of a Gemini session file.
type GeminiSession struct {
	SessionID string          `json:"sessionId"`
	Messages  []GeminiMessage `json:"messages"`
}

// PollGeminiSession reads a Gemini session file and returns messages since
// the given index. Returns the new message count.
func PollGeminiSession(path string, sinceIdx int) ([]GeminiMessage, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, sinceIdx, err
	}
	var session GeminiSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, sinceIdx, err
	}
	if len(session.Messages) <= sinceIdx {
		return nil, sinceIdx, nil
	}
	newMsgs := session.Messages[sinceIdx:]
	return newMsgs, len(session.Messages), nil
}
