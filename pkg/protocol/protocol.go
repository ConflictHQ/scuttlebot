// Package protocol defines the scuttlebot wire format.
//
// Agent messages are JSON envelopes sent as IRC PRIVMSG.
// System/status messages use NOTICE and are human-readable only.
package protocol

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"github.com/oklog/ulid/v2"
)

// Version is the current envelope version.
const Version = 1

// Message types.
const (
	TypeTaskCreate   = "task.create"
	TypeTaskUpdate   = "task.update"
	TypeTaskComplete = "task.complete"
	TypeAgentHello   = "agent.hello"
	TypeAgentBye     = "agent.bye"
)

// Envelope is the standard wrapper for all agent messages over IRC.
type Envelope struct {
	V       int             `json:"v"`
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	From    string          `json:"from"`
	TS      int64           `json:"ts"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// New creates a new Envelope with a generated ID and current timestamp.
func New(msgType, from string, payload any) (*Envelope, error) {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("protocol: marshal payload: %w", err)
		}
		raw = b
	}
	return &Envelope{
		V:       Version,
		Type:    msgType,
		ID:      newID(),
		From:    from,
		TS:      time.Now().UnixMilli(),
		Payload: raw,
	}, nil
}

// Marshal encodes the envelope to JSON.
func Marshal(e *Envelope) ([]byte, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("protocol: marshal envelope: %w", err)
	}
	return b, nil
}

// Unmarshal decodes a JSON envelope and validates it.
func Unmarshal(data []byte) (*Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("protocol: unmarshal envelope: %w", err)
	}
	if err := validate(&e); err != nil {
		return nil, err
	}
	return &e, nil
}

// UnmarshalPayload decodes the envelope payload into dst.
func UnmarshalPayload(e *Envelope, dst any) error {
	if len(e.Payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(e.Payload, dst); err != nil {
		return fmt.Errorf("protocol: unmarshal payload: %w", err)
	}
	return nil
}

func validate(e *Envelope) error {
	if e.V != Version {
		return fmt.Errorf("protocol: unsupported version %d", e.V)
	}
	if e.Type == "" {
		return fmt.Errorf("protocol: missing type")
	}
	if e.ID == "" {
		return fmt.Errorf("protocol: missing id")
	}
	if e.From == "" {
		return fmt.Errorf("protocol: missing from")
	}
	return nil
}

func newID() string {
	entropy := ulid.Monotonic(rand.New(rand.NewSource(time.Now().UnixNano())), 0) //nolint:gosec
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
}
