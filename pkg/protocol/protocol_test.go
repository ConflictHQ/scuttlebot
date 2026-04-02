package protocol_test

import (
	"encoding/json"
	"testing"

	"github.com/conflicthq/scuttlebot/pkg/protocol"
)

func TestRoundTrip(t *testing.T) {
	type testPayload struct {
		Task string `json:"task"`
	}

	env, err := protocol.New(protocol.TypeTaskCreate, "claude-01", testPayload{Task: "write tests"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	data, err := protocol.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got, err := protocol.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.V != protocol.Version {
		t.Errorf("V: got %d, want %d", got.V, protocol.Version)
	}
	if got.Type != protocol.TypeTaskCreate {
		t.Errorf("Type: got %q, want %q", got.Type, protocol.TypeTaskCreate)
	}
	if got.ID == "" {
		t.Error("ID is empty")
	}
	if got.From != "claude-01" {
		t.Errorf("From: got %q, want %q", got.From, "claude-01")
	}
	if got.TS == 0 {
		t.Error("TS is zero")
	}

	var p testPayload
	if err := protocol.UnmarshalPayload(got, &p); err != nil {
		t.Fatalf("UnmarshalPayload: %v", err)
	}
	if p.Task != "write tests" {
		t.Errorf("payload.Task: got %q, want %q", p.Task, "write tests")
	}
}

func TestUnmarshalInvalid(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"not json", `not json`},
		{"wrong version", `{"v":99,"type":"task.create","id":"01HX","from":"agent","ts":1}`},
		{"missing type", `{"v":1,"id":"01HX","from":"agent","ts":1}`},
		{"missing id", `{"v":1,"type":"task.create","from":"agent","ts":1}`},
		{"missing from", `{"v":1,"type":"task.create","id":"01HX","ts":1}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := protocol.Unmarshal([]byte(tc.json))
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestNewGeneratesUniqueIDs(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		env, err := protocol.New(protocol.TypeAgentHello, "agent", nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if seen[env.ID] {
			t.Errorf("duplicate ID: %s", env.ID)
		}
		seen[env.ID] = true
	}
}

func TestNilPayload(t *testing.T) {
	env, err := protocol.New(protocol.TypeAgentBye, "agent-01", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	data, err := protocol.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got, err := protocol.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if len(got.Payload) != 0 {
		t.Errorf("expected empty payload, got %s", got.Payload)
	}
}

func TestMatchesRecipient(t *testing.T) {
	cases := []struct {
		name      string
		to        []string
		nick      string
		agentType string
		want      bool
	}{
		// backwards compat
		{"empty to matches all", nil, "claude-1", "worker", true},

		// @all
		{"@all matches worker", []string{"@all"}, "claude-1", "worker", true},
		{"@all matches operator", []string{"@all"}, "glengoolie", "operator", true},

		// role tokens
		{"@workers matches worker", []string{"@workers"}, "claude-1", "worker", true},
		{"@workers no match orchestrator", []string{"@workers"}, "claude-1", "orchestrator", false},
		{"@operators matches operator", []string{"@operators"}, "glengoolie", "operator", true},
		{"@orchestrators matches orchestrator", []string{"@orchestrators"}, "claude-1", "orchestrator", true},
		{"@observers matches observer", []string{"@observers"}, "sentinel", "observer", true},

		// prefix glob
		{"@claude-* matches claude-1", []string{"@claude-*"}, "claude-1", "worker", true},
		{"@claude-* matches claude-sonnet", []string{"@claude-*"}, "claude-sonnet", "worker", true},
		{"@claude-* no match codex-1", []string{"@claude-*"}, "codex-1", "worker", false},
		{"@gemini-* matches gemini-pro", []string{"@gemini-*"}, "gemini-pro", "worker", true},

		// exact nick
		{"exact nick match", []string{"codex-7"}, "codex-7", "worker", true},
		{"exact nick no match", []string{"codex-7"}, "codex-8", "worker", false},

		// OR semantics
		{"OR: second token matches", []string{"@operators", "codex-7"}, "codex-7", "worker", true},
		{"OR: first token matches", []string{"@workers", "codex-7"}, "claude-1", "worker", true},
		{"OR: none match", []string{"@operators", "codex-7"}, "claude-1", "worker", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := &protocol.Envelope{
				V:    protocol.Version,
				Type: protocol.TypeTaskCreate,
				ID:   "test",
				From: "orchestrator",
				To:   tc.to,
				TS:   1,
			}
			got := protocol.MatchesRecipient(env, tc.nick, tc.agentType)
			if got != tc.want {
				t.Errorf("MatchesRecipient(%v, %q, %q) = %v, want %v", tc.to, tc.nick, tc.agentType, got, tc.want)
			}
		})
	}
}

func TestNewTo(t *testing.T) {
	env, err := protocol.NewTo(protocol.TypeTaskCreate, "orchestrator-1", []string{"@workers", "@claude-*"}, nil)
	if err != nil {
		t.Fatalf("NewTo: %v", err)
	}
	if len(env.To) != 2 {
		t.Fatalf("To length: got %d, want 2", len(env.To))
	}
	if env.To[0] != "@workers" || env.To[1] != "@claude-*" {
		t.Errorf("To: got %v", env.To)
	}

	// round-trip
	data, err := protocol.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := protocol.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.To) != 2 || got.To[0] != "@workers" {
		t.Errorf("round-trip To: got %v", got.To)
	}
}

func TestToOmittedWhenEmpty(t *testing.T) {
	env, err := protocol.New(protocol.TypeAgentHello, "agent", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	data, err := protocol.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := raw["to"]; ok {
		t.Error("expected 'to' key to be omitted when empty")
	}
}

func TestAllMessageTypes(t *testing.T) {
	types := []string{
		protocol.TypeTaskCreate,
		protocol.TypeTaskUpdate,
		protocol.TypeTaskComplete,
		protocol.TypeAgentHello,
		protocol.TypeAgentBye,
	}
	for _, msgType := range types {
		t.Run(msgType, func(t *testing.T) {
			env, err := protocol.New(msgType, "agent", json.RawMessage(`{"key":"val"}`))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			data, err := protocol.Marshal(env)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			got, err := protocol.Unmarshal(data)
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got.Type != msgType {
				t.Errorf("Type: got %q, want %q", got.Type, msgType)
			}
		})
	}
}
