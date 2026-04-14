package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFilterMessages(t *testing.T) {
	t.Helper()

	base := time.Date(2026, 3, 31, 21, 0, 0, 0, time.FixedZone("CST", -6*60*60))
	since := base.Add(-time.Second)
	nick := "codex-scuttlebot-1234"

	messages := []message{
		{Nick: "bridge", Text: "[glengoolie] hello", At: base},
		{Nick: "glengoolie", Text: "ambient chat", At: base.Add(time.Second)},
		{Nick: "codex-otherrepo-9999", Text: "status post", At: base.Add(2 * time.Second)},
		{Nick: "glengoolie", Text: nick + ": check README.md", At: base.Add(3 * time.Second)},
		{Nick: "glengoolie", Text: nick + ": and inspect bridge.go", At: base.Add(4 * time.Second)},
	}

	got, newest := filterMessages(messages, since, nick, "worker")
	if len(got) != 2 {
		t.Fatalf("len(filterMessages) = %d, want 2", len(got))
	}
	if got[0].Text != nick+": check README.md" {
		t.Fatalf("first injected message = %q", got[0].Text)
	}
	if got[1].Text != nick+": and inspect bridge.go" {
		t.Fatalf("second injected message = %q", got[1].Text)
	}
	if !newest.Equal(base.Add(4 * time.Second)) {
		t.Fatalf("newest = %s", newest)
	}
}

func TestTargetCWD(t *testing.T) {
	t.Helper()

	cwd, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}

	got, err := targetCWD([]string{"--cd", "../.."})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Clean(filepath.Join(cwd, "../.."))
	if got != want {
		t.Fatalf("targetCWD = %q, want %q", got, want)
	}
}

func TestRelayStateShouldInterruptOnlyWhenRecentlyBusy(t *testing.T) {
	t.Helper()

	var state relayState
	now := time.Date(2026, 3, 31, 21, 47, 0, 0, time.UTC)
	state.observeOutput([]byte("Working (1s • esc to interrupt)"), now)

	if !state.shouldInterrupt(now.Add(defaultBusyWindow / 2)) {
		t.Fatal("shouldInterrupt = false, want true for recent busy session")
	}
	if state.shouldInterrupt(now.Add(defaultBusyWindow + time.Millisecond)) {
		t.Fatal("shouldInterrupt = true, want false after busy window expires")
	}
}

func TestInjectMessagesIdleSkipsCtrlCAndSubmits(t *testing.T) {
	t.Helper()

	var writer bytes.Buffer
	cfg := config{
		Nick:               "codex-scuttlebot-1234",
		InterruptOnMessage: true,
	}
	state := &relayState{}
	batch := []message{{
		Nick: "glengoolie",
		Text: "codex-scuttlebot-1234: check README.md",
	}}

	if err := injectMessages(&writer, cfg, state, "#general", batch); err != nil {
		t.Fatal(err)
	}

	want := "[IRC operator messages]\n[general] glengoolie: check README.md\n\r"
	if writer.String() != want {
		t.Fatalf("injectMessages idle = %q, want %q", writer.String(), want)
	}
}

func TestInjectMessagesBusySendsCtrlCBeforeSubmit(t *testing.T) {
	t.Helper()

	var writer bytes.Buffer
	cfg := config{
		Nick:               "codex-scuttlebot-1234",
		InterruptOnMessage: true,
	}
	state := &relayState{}
	state.observeOutput([]byte("Working (2s • esc to interrupt)"), time.Now())
	batch := []message{{
		Nick: "glengoolie",
		Text: "codex-scuttlebot-1234: stop and re-read bridge.go",
	}}

	if err := injectMessages(&writer, cfg, state, "#general", batch); err != nil {
		t.Fatal(err)
	}

	want := string([]byte{3}) + "[IRC operator messages]\n[general] glengoolie: stop and re-read bridge.go\n\r"
	if writer.String() != want {
		t.Fatalf("injectMessages busy = %q, want %q", writer.String(), want)
	}
}

func TestSummarizeFunctionCallExecCommandRedactsSecrets(t *testing.T) {
	t.Helper()

	msg := summarizeFunctionCall("exec_command", `{"cmd":"cd /repo && curl -H \"Authorization: Bearer d2f5565f5f34fe6ea81d3cba6c20117f032180e3cf4aa401\" http://localhost:8080/v1/status"}`)
	if !strings.HasPrefix(msg, "› curl") {
		t.Fatalf("summarizeFunctionCall prefix = %q", msg)
	}
	if strings.Contains(msg, "d2f5565f5f34fe6ea81d3cba6c20117f032180e3cf4aa401") {
		t.Fatalf("summarizeFunctionCall leaked token: %q", msg)
	}
	if !strings.Contains(msg, "[redacted]") {
		t.Fatalf("summarizeFunctionCall did not redact secret: %q", msg)
	}
}

func TestSummarizeCustomToolCallApplyPatch(t *testing.T) {
	t.Helper()

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: cmd/codex-relay/main.go",
		"*** Add File: glengoolie.tmp",
		"*** End Patch",
	}, "\n")

	got := summarizeCustomToolCall("apply_patch", patch)
	want := "patch 2 files: cmd/codex-relay/main.go, glengoolie.tmp"
	if got != want {
		t.Fatalf("summarizeCustomToolCall = %q, want %q", got, want)
	}
}

func TestSessionMessagesFunctionCallAndAssistant(t *testing.T) {
	t.Helper()

	fnLine := []byte(`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"}}`)
	got := sessionMessages(fnLine, false, time.Time{})
	if len(got) != 1 || got[0].Text != "› pwd" {
		t.Fatalf("sessionMessages function_call = %#v", got)
	}

	msgLine := []byte(`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"one line\nsecond line"}]}}`)
	got = sessionMessages(msgLine, false, time.Time{})
	if len(got) != 2 || got[0].Text != "one line" || got[1].Text != "second line" {
		t.Fatalf("sessionMessages assistant = %#v", got)
	}
}

func TestSessionMessagesReasoning(t *testing.T) {
	line := []byte(`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"reasoning","text":"thinking hard"},{"type":"output_text","text":"done"}]}}`)

	// reasoning off — only output_text
	got := sessionMessages(line, false, time.Time{})
	if len(got) != 1 || got[0].Text != "done" {
		t.Fatalf("mirrorReasoning=false: got %#v", got)
	}

	// reasoning on — both, reasoning prefixed
	got = sessionMessages(line, true, time.Time{})
	if len(got) != 2 || got[0].Text != "💭 thinking hard" || got[1].Text != "done" {
		t.Fatalf("mirrorReasoning=true: got %#v", got)
	}
}

func TestExplicitThreadID(t *testing.T) {
	t.Helper()

	got := explicitThreadID([]string{"resume", "019d45e1-8328-7261-9a02-5c4304e07724"})
	want := "019d45e1-8328-7261-9a02-5c4304e07724"
	if got != want {
		t.Fatalf("explicitThreadID = %q, want %q", got, want)
	}
}

func writeSessionFile(t *testing.T, dir, uuid, cwd, timestamp string) string {
	t.Helper()
	content := fmt.Sprintf(`{"type":"session_meta","payload":{"id":"%s","timestamp":"%s","cwd":"%s"}}`, uuid, timestamp, cwd)
	name := fmt.Sprintf("rollout-%s-%s.jsonl", strings.ReplaceAll(timestamp[:19], ":", "-"), uuid)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFindLatestSessionPathSkipsPreExisting(t *testing.T) {
	t.Helper()

	root := t.TempDir()
	dateDir := filepath.Join(root, "2026", "04", "04")
	if err := os.MkdirAll(dateDir, 0755); err != nil {
		t.Fatal(err)
	}

	cwd := "/home/user/project"

	// Create a pre-existing session file.
	oldPath := writeSessionFile(t, dateDir,
		"aaaa-aaaa-aaaa-aaaa", cwd, "2026-04-04T10:00:00Z")

	// Snapshot includes the old file.
	preExisting := map[string]struct{}{oldPath: {}}

	// Create a new session file (not in snapshot).
	newPath := writeSessionFile(t, dateDir,
		"bbbb-bbbb-bbbb-bbbb", cwd, "2026-04-04T10:00:01Z")

	notBefore, _ := time.Parse(time.RFC3339, "2026-04-04T09:59:58Z")
	got, err := findLatestSessionPath(root, cwd, notBefore, preExisting)
	if err != nil {
		t.Fatalf("findLatestSessionPath error: %v", err)
	}
	if got != newPath {
		t.Fatalf("findLatestSessionPath = %q, want %q", got, newPath)
	}
}

func TestFindLatestSessionPathPicksOldestNew(t *testing.T) {
	t.Helper()

	root := t.TempDir()
	dateDir := filepath.Join(root, "2026", "04", "04")
	if err := os.MkdirAll(dateDir, 0755); err != nil {
		t.Fatal(err)
	}

	cwd := "/home/user/project"

	// Two new sessions in the same CWD, no pre-existing.
	earlyPath := writeSessionFile(t, dateDir,
		"cccc-cccc-cccc-cccc", cwd, "2026-04-04T10:00:01Z")
	_ = writeSessionFile(t, dateDir,
		"dddd-dddd-dddd-dddd", cwd, "2026-04-04T10:00:02Z")

	notBefore, _ := time.Parse(time.RFC3339, "2026-04-04T10:00:00Z")
	got, err := findLatestSessionPath(root, cwd, notBefore, map[string]struct{}{})
	if err != nil {
		t.Fatalf("findLatestSessionPath error: %v", err)
	}
	if got != earlyPath {
		t.Fatalf("findLatestSessionPath = %q, want oldest %q", got, earlyPath)
	}
}

func TestFindLatestSessionPathNilPreExistingAllowsAll(t *testing.T) {
	t.Helper()

	root := t.TempDir()
	dateDir := filepath.Join(root, "2026", "04", "04")
	if err := os.MkdirAll(dateDir, 0755); err != nil {
		t.Fatal(err)
	}

	cwd := "/home/user/project"

	// Single file — nil preExisting (reconnect path) should find it.
	path := writeSessionFile(t, dateDir,
		"eeee-eeee-eeee-eeee", cwd, "2026-04-04T10:00:00Z")

	got, err := findLatestSessionPath(root, cwd, time.Time{}, nil)
	if err != nil {
		t.Fatalf("findLatestSessionPath error: %v", err)
	}
	if got != path {
		t.Fatalf("findLatestSessionPath = %q, want %q", got, path)
	}
}

func TestSnapshotSessionFiles(t *testing.T) {
	t.Helper()

	root := t.TempDir()
	dateDir := filepath.Join(root, "2026", "04", "04")
	if err := os.MkdirAll(dateDir, 0755); err != nil {
		t.Fatal(err)
	}

	path := writeSessionFile(t, dateDir,
		"ffff-ffff-ffff-ffff", "/tmp", "2026-04-04T10:00:00Z")

	snap := snapshotSessionFiles(root)
	if _, ok := snap[path]; !ok {
		t.Fatalf("snapshotSessionFiles missing %q", path)
	}
	if len(snap) != 1 {
		t.Fatalf("snapshotSessionFiles len = %d, want 1", len(snap))
	}
}
