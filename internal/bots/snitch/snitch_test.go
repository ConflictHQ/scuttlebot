package snitch_test

import (
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/internal/bots/snitch"
)

func TestNewBotDefaults(t *testing.T) {
	b := snitch.New(snitch.Config{IRCAddr: "127.0.0.1:6667"}, nil)
	if b == nil {
		t.Fatal("New returned nil")
	}
}

func TestNewBotCustomThresholds(t *testing.T) {
	cfg := snitch.Config{
		IRCAddr:           "127.0.0.1:6667",
		Nick:              "snitch",
		Password:          "pw",
		FloodMessages:     5,
		FloodWindow:       2 * time.Second,
		JoinPartThreshold: 3,
		JoinPartWindow:    10 * time.Second,
	}
	b := snitch.New(cfg, nil)
	if b == nil {
		t.Fatal("New returned nil")
	}
}

func TestMultipleBotInstancesAreDistinct(t *testing.T) {
	b1 := snitch.New(snitch.Config{IRCAddr: "127.0.0.1:6667", Nick: "snitch1"}, nil)
	b2 := snitch.New(snitch.Config{IRCAddr: "127.0.0.1:6667", Nick: "snitch2"}, nil)
	if b1 == b2 {
		t.Error("expected distinct bot instances")
	}
}
