package relaymirror

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPollGeminiSessionWithThoughts(t *testing.T) {
	session := GeminiSession{
		SessionID: "test-session",
		Messages: []GeminiMessage{
			{Type: "user", Content: "hello"},
			{
				Type:    "gemini",
				Content: "I'll help with that.",
				Thoughts: []GeminiThought{
					{
						Subject:     "Understanding the request",
						Description: "The user wants help with a task.",
						Timestamp:   "2026-04-08T12:00:00.000Z",
					},
					{
						Subject:     "Planning approach",
						Description: "I should break this down step by step.",
					},
				},
				ToolCalls: []GeminiToolCall{
					{
						Name:   "readFile",
						Args:   json.RawMessage(`{"path":"main.go"}`),
						Status: "completed",
					},
				},
			},
		},
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "session-test.json")
	data, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	// Poll from index 0 to get all messages.
	msgs, newIdx, err := PollGeminiSession(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if newIdx != 2 {
		t.Errorf("expected newIdx=2, got %d", newIdx)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	geminiMsg := msgs[1]
	if geminiMsg.Type != "gemini" {
		t.Errorf("expected type=gemini, got %s", geminiMsg.Type)
	}
	if geminiMsg.Content != "I'll help with that." {
		t.Errorf("unexpected content: %s", geminiMsg.Content)
	}
	if len(geminiMsg.Thoughts) != 2 {
		t.Fatalf("expected 2 thoughts, got %d", len(geminiMsg.Thoughts))
	}
	if geminiMsg.Thoughts[0].Subject != "Understanding the request" {
		t.Errorf("thought[0].Subject = %q", geminiMsg.Thoughts[0].Subject)
	}
	if geminiMsg.Thoughts[0].Description != "The user wants help with a task." {
		t.Errorf("thought[0].Description = %q", geminiMsg.Thoughts[0].Description)
	}
	if geminiMsg.Thoughts[1].Subject != "Planning approach" {
		t.Errorf("thought[1].Subject = %q", geminiMsg.Thoughts[1].Subject)
	}
	if len(geminiMsg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(geminiMsg.ToolCalls))
	}

	// Poll from index 2 should return nothing new.
	msgs2, newIdx2, err := PollGeminiSession(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if newIdx2 != 2 {
		t.Errorf("expected newIdx=2, got %d", newIdx2)
	}
	if len(msgs2) != 0 {
		t.Errorf("expected 0 new messages, got %d", len(msgs2))
	}
}

func TestGeminiMessageArrayContent(t *testing.T) {
	// User-type messages in real Gemini session files have `content` as an
	// array of {text: "..."} parts rather than a plain string.
	raw := `{"messages":[{"type":"user","content":[{"text":"hi there"}]},{"type":"gemini","content":"hello back"}]}`
	var s GeminiSession
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(s.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(s.Messages))
	}
	if s.Messages[0].Content != "hi there" {
		t.Errorf("user content = %q, want %q", s.Messages[0].Content, "hi there")
	}
	if s.Messages[1].Content != "hello back" {
		t.Errorf("gemini content = %q, want %q", s.Messages[1].Content, "hello back")
	}
}

func TestGeminiMessageNoThoughts(t *testing.T) {
	// Verify that messages without thoughts deserialize cleanly.
	raw := `{"type":"gemini","content":"hello","toolCalls":[{"name":"ls","args":{},"status":"done"}]}`
	var msg GeminiMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	if len(msg.Thoughts) != 0 {
		t.Errorf("expected 0 thoughts, got %d", len(msg.Thoughts))
	}
	if msg.Content != "hello" {
		t.Errorf("content = %q", msg.Content)
	}
	if len(msg.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(msg.ToolCalls))
	}
}

// Real on-disk JSONL session content from Gemini CLI 0.40.1 — captured from
// a running pod. Three line types: header (no "type"), user/gemini message
// rows, and {"$set": ...} bookkeeping rows. Only the type-bearing rows
// should be returned as messages.
func TestPollGeminiSessionJSONL(t *testing.T) {
	jsonl := `{"sessionId":"b1060ec9-a6fa-4446-93dd-1b3c052e4531","projectHash":"4b8ac3","startTime":"2026-05-03T16:15:38.636Z","lastUpdated":"2026-05-03T16:15:38.636Z","kind":"main"}
{"id":"02d1610a","timestamp":"2026-05-03T16:16:52.623Z","type":"user","content":[{"text":"[IRC operator messages]\n[general] lmata: hi perform no further actions"}]}
{"$set":{"lastUpdated":"2026-05-03T16:16:52.629Z"}}
{"id":"1a9a3caf","timestamp":"2026-05-03T16:16:55.745Z","type":"gemini","content":"Acknowledged. I will wait for your next instruction.","thoughts":[{"subject":"Assessing the Ambiguity","description":"I'm currently parsing the user's message.","timestamp":"2026-05-03T16:16:54.956Z"}],"tokens":{"input":15475,"output":11,"cached":0,"thoughts":290,"tool":0,"total":15776},"model":"gemini-3-flash-preview"}
{"$set":{"lastUpdated":"2026-05-03T16:16:55.750Z"}}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "session-2026-05-03T16-15-b1060ec9.jsonl")
	if err := os.WriteFile(path, []byte(jsonl), 0644); err != nil {
		t.Fatal(err)
	}

	msgs, idx, err := PollGeminiSession(path, 0)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if idx != 2 {
		t.Errorf("expected total=2 type-bearing messages, got %d", idx)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 new messages, got %d", len(msgs))
	}
	if msgs[0].Type != "user" {
		t.Errorf("msg[0].Type = %q", msgs[0].Type)
	}
	if !contains(msgs[0].Content, "[IRC operator messages]") {
		t.Errorf("msg[0].Content missing IRC prefix: %q", msgs[0].Content)
	}
	if msgs[1].Type != "gemini" {
		t.Errorf("msg[1].Type = %q", msgs[1].Type)
	}
	if msgs[1].Content != "Acknowledged. I will wait for your next instruction." {
		t.Errorf("msg[1].Content = %q", msgs[1].Content)
	}
	if len(msgs[1].Thoughts) != 1 || msgs[1].Thoughts[0].Subject != "Assessing the Ambiguity" {
		t.Errorf("msg[1].Thoughts = %#v", msgs[1].Thoughts)
	}

	// Polling again with sinceIdx=2 returns nothing new but preserves count.
	msgs2, idx2, err := PollGeminiSession(path, 2)
	if err != nil {
		t.Fatalf("poll-2: %v", err)
	}
	if len(msgs2) != 0 || idx2 != 2 {
		t.Errorf("expected (0,2) on re-poll, got (%d,%d)", len(msgs2), idx2)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || (len(s) > len(substr) && (indexOf(s, substr) >= 0)))
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
