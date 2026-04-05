// Package relaymirror provides shared PTY output mirroring for relay binaries.
//
// PTYMirror reads from a PTY file descriptor and emits lines to a callback.
// It handles ANSI escape stripping and line buffering for clean IRC output.
package relaymirror

import (
	"bytes"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ansiRE matches ANSI escape sequences (including partial/split ones).
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\].*?\x07|\x1b\(B|\[\?[0-9]+[hl]`)

// noiseRE matches common terminal noise: spinner chars, progress bars, cursor movement.
var noiseRE = regexp.MustCompile(`^[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏\-\\|/]+$|^\s*\d+%\s*$|^[.]+$|^\[?\?[0-9]+[hl]`)

// PTYMirror reads PTY output and emits clean text lines to IRC.
// It includes rate limiting and noise filtering for clean IRC output.
type PTYMirror struct {
	maxLineLen  int
	minInterval time.Duration // minimum time between emitted lines
	mu          sync.Mutex
	lastEmit    time.Time
	recentLines map[string]time.Time // dedup: line hash → last seen
	onLine      func(line string)
	// BusyCallback is called when PTY output suggests the agent is busy
	// (e.g. "esc to interrupt", "working..."). Optional.
	BusyCallback func(now time.Time)
}

// NewPTYMirror creates a mirror that calls onLine for each output line.
// maxLineLen truncates long lines (0 = no limit).
// minInterval throttles output (0 = no throttle, recommended: 500ms for IRC).
func NewPTYMirror(maxLineLen int, minInterval time.Duration, onLine func(line string)) *PTYMirror {
	return &PTYMirror{
		maxLineLen:  maxLineLen,
		minInterval: minInterval,
		recentLines: make(map[string]time.Time),
		onLine:      onLine,
	}
}

// Copy reads from r (typically a PTY fd) and also writes to w (typically
// os.Stdout for the interactive terminal). Lines are emitted via onLine.
// Blocks until r returns EOF or error.
func (m *PTYMirror) Copy(r io.Reader, w io.Writer) error {
	buf := make([]byte, 4096)
	lineCh := make(chan []byte, 64) // buffered channel for async line processing
	done := make(chan struct{})

	// Process lines in a separate goroutine so terminal is never blocked.
	go func() {
		defer close(done)
		var lineBuf bytes.Buffer
		for chunk := range lineCh {
			lineBuf.Write(chunk)
			m.emitLines(&lineBuf)
		}
		if lineBuf.Len() > 0 {
			m.emitLine(lineBuf.String())
		}
	}()

	for {
		n, err := r.Read(buf)
		if n > 0 {
			// Detect busy signals for interrupt logic.
			if m.BusyCallback != nil {
				lower := strings.ToLower(string(buf[:n]))
				if strings.Contains(lower, "esc to interrupt") || strings.Contains(lower, "working...") {
					m.BusyCallback(time.Now())
				}
			}
			// Pass through to terminal — ALWAYS immediate, never blocked.
			if w != nil {
				_, _ = w.Write(buf[:n])
			}
			// Send to line processor (non-blocking with buffered channel).
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case lineCh <- chunk:
			default:
				// Channel full — drop this chunk rather than block terminal.
			}
		}
		if err != nil {
			close(lineCh)
			<-done
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func (m *PTYMirror) emitLines(buf *bytes.Buffer) {
	for {
		line, err := buf.ReadString('\n')
		if err != nil {
			// No newline found — put back the partial line.
			buf.WriteString(line)
			return
		}
		m.emitLine(line)
	}
}

func (m *PTYMirror) emitLine(raw string) {
	// Strip ANSI escapes and carriage returns.
	clean := ansiRE.ReplaceAllString(raw, "")
	clean = strings.ReplaceAll(clean, "\r", "")
	clean = strings.TrimRight(clean, "\n")
	clean = strings.TrimSpace(clean)

	if clean == "" {
		return
	}
	// Skip terminal noise (spinners, progress bars, dots).
	if noiseRE.MatchString(clean) {
		return
	}
	if m.maxLineLen > 0 && len(clean) > m.maxLineLen {
		clean = clean[:m.maxLineLen-3] + "..."
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	// Rate limit.
	if m.minInterval > 0 && now.Sub(m.lastEmit) < m.minInterval {
		return
	}
	// Dedup: skip if we've seen this exact line in the last 5 seconds.
	if seen, ok := m.recentLines[clean]; ok && now.Sub(seen) < 5*time.Second {
		return
	}
	m.recentLines[clean] = now
	m.lastEmit = now

	// Prune old dedup entries.
	if len(m.recentLines) > 200 {
		for k, v := range m.recentLines {
			if now.Sub(v) > 10*time.Second {
				delete(m.recentLines, k)
			}
		}
	}

	m.onLine(clean)
}

// MarkSeen records a line as recently seen for dedup purposes.
// Call this when the session file mirror emits a line so the PTY mirror
// won't duplicate it.
func (m *PTYMirror) MarkSeen(line string) {
	m.mu.Lock()
	m.recentLines[strings.TrimSpace(line)] = time.Now()
	m.mu.Unlock()
}

// StripANSI removes ANSI escape sequences from a string.
func StripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}
