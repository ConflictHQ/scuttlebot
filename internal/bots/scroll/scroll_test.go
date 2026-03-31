package scroll_test

import (
	"strings"
	"testing"

	"github.com/conflicthq/scuttlebot/internal/bots/scroll"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantCh  string
		wantLim int
		wantErr bool
	}{
		{"basic", "replay #fleet", "#fleet", 50, false},
		{"with last", "replay #fleet last=100", "#fleet", 100, false},
		{"last capped at max", "replay #fleet last=9999", "#fleet", 500, false},
		{"missing channel", "replay", "", 0, true},
		{"no hash", "replay fleet", "", 0, true},
		{"unknown command", "history #fleet", "", 0, true},
		{"invalid last", "replay #fleet last=abc", "", 0, true},
		{"unknown arg", "replay #fleet foo=bar", "", 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := scroll.ParseCommand(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ParseCommand(%q): expected error, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCommand(%q): unexpected error: %v", tc.input, err)
			}
			if req.Channel != tc.wantCh {
				t.Errorf("Channel: got %q, want %q", req.Channel, tc.wantCh)
			}
			if req.Limit != tc.wantLim {
				t.Errorf("Limit: got %d, want %d", req.Limit, tc.wantLim)
			}
		})
	}
}

func TestParseCommandCaseInsensitive(t *testing.T) {
	req, err := scroll.ParseCommand("REPLAY #fleet last=10")
	if err != nil {
		t.Fatalf("ParseCommand: %v", err)
	}
	if req.Channel != "#fleet" {
		t.Errorf("Channel: got %q", req.Channel)
	}
	_ = strings.ToLower // just confirming case insensitivity is tested
}
