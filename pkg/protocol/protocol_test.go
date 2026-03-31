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
