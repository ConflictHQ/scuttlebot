package relaymirror

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestPTYMirrorBasic(t *testing.T) {
	var lines []string
	m := NewPTYMirror(0, 0, func(line string) {
		lines = append(lines, line)
	})

	input := "hello world\ngoodbye\n"
	err := m.Copy(strings.NewReader(input), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "hello world" {
		t.Errorf("line 0 = %q", lines[0])
	}
	if lines[1] != "goodbye" {
		t.Errorf("line 1 = %q", lines[1])
	}
}

func TestPTYMirrorStripANSI(t *testing.T) {
	var lines []string
	m := NewPTYMirror(0, 0, func(line string) {
		lines = append(lines, line)
	})

	input := "\x1b[32mgreen text\x1b[0m\n"
	_ = m.Copy(strings.NewReader(input), nil)

	if len(lines) != 1 || lines[0] != "green text" {
		t.Errorf("expected 'green text', got %v", lines)
	}
}

func TestPTYMirrorPassthrough(t *testing.T) {
	var buf bytes.Buffer
	m := NewPTYMirror(0, 0, func(string) {})

	input := "hello\n"
	_ = m.Copy(strings.NewReader(input), &buf)

	if buf.String() != input {
		t.Errorf("passthrough = %q, want %q", buf.String(), input)
	}
}

func TestPTYMirrorMaxLineLen(t *testing.T) {
	var lines []string
	m := NewPTYMirror(20, 0, func(line string) {
		lines = append(lines, line)
	})

	input := "this is a very long line that should be truncated\n"
	_ = m.Copy(strings.NewReader(input), nil)

	if len(lines) != 1 || len(lines[0]) > 20 {
		t.Errorf("expected truncated line <= 20 chars, got %q", lines[0])
	}
}

func TestPTYMirrorNoise(t *testing.T) {
	var lines []string
	m := NewPTYMirror(0, 0, func(line string) {
		lines = append(lines, line)
	})

	input := "real output\n⠋\n...\n50%\nmore output\n"
	_ = m.Copy(strings.NewReader(input), nil)

	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (noise filtered), got %d: %v", len(lines), lines)
	}
}

func TestPTYMirrorDedup(t *testing.T) {
	var lines []string
	m := NewPTYMirror(0, 0, func(line string) {
		lines = append(lines, line)
	})

	input := "same line\nsame line\ndifferent\n"
	_ = m.Copy(strings.NewReader(input), nil)

	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (dedup), got %d: %v", len(lines), lines)
	}
}

func TestPTYMirrorMarkSeen(t *testing.T) {
	var lines []string
	m := NewPTYMirror(0, 0, func(line string) {
		lines = append(lines, line)
	})

	// Mark a line as seen (from session file mirror).
	m.MarkSeen("already seen")

	input := "already seen\nnew line\n"
	_ = m.Copy(strings.NewReader(input), nil)

	if len(lines) != 1 || lines[0] != "new line" {
		t.Errorf("expected only 'new line', got %v", lines)
	}
}

func TestPTYMirrorBusyCallback(t *testing.T) {
	var busyAt time.Time
	m := NewPTYMirror(0, 0, func(string) {})
	m.BusyCallback = func(now time.Time) { busyAt = now }

	input := "esc to interrupt\n"
	_ = m.Copy(strings.NewReader(input), nil)

	if busyAt.IsZero() {
		t.Error("busy callback was not called")
	}
}
