package api

import (
	"testing"

	"github.com/conflicthq/scuttlebot/internal/registry"
)

func TestFilterAgentsByTeam(t *testing.T) {
	agents := []*registry.Agent{
		{Nick: "a1", Team: "alpha"},
		{Nick: "a2", Team: "alpha"},
		{Nick: "b1", Team: "bravo"},
		{Nick: "no-team", Team: ""},
	}

	tests := []struct {
		name      string
		keyTeam   string
		queryTeam string
		wantNicks []string
	}{
		{
			name:      "unrestricted key, no query: all agents",
			keyTeam:   "",
			queryTeam: "",
			wantNicks: []string{"a1", "a2", "b1", "no-team"},
		},
		{
			name:      "unrestricted key, query alpha: only alpha",
			keyTeam:   "",
			queryTeam: "alpha",
			wantNicks: []string{"a1", "a2"},
		},
		{
			name:      "team-scoped key, no query: only that team",
			keyTeam:   "alpha",
			queryTeam: "",
			wantNicks: []string{"a1", "a2"},
		},
		{
			name:      "team-scoped key, same query: only that team",
			keyTeam:   "alpha",
			queryTeam: "alpha",
			wantNicks: []string{"a1", "a2"},
		},
		{
			name:      "team-scoped key, different query: empty",
			keyTeam:   "alpha",
			queryTeam: "bravo",
			wantNicks: []string{},
		},
		{
			name:      "team-scoped key, case insensitive match",
			keyTeam:   "Alpha",
			queryTeam: "",
			wantNicks: []string{"a1", "a2"},
		},
		{
			name:      "unrestricted key, query for non-existent team: empty",
			keyTeam:   "",
			queryTeam: "ghost",
			wantNicks: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterAgentsByTeam(agents, tt.keyTeam, tt.queryTeam)
			if len(got) != len(tt.wantNicks) {
				t.Fatalf("got %d agents, want %d", len(got), len(tt.wantNicks))
			}
			for i, a := range got {
				if a.Nick != tt.wantNicks[i] {
					t.Errorf("agent[%d]: got nick %q, want %q", i, a.Nick, tt.wantNicks[i])
				}
			}
		})
	}
}

func TestFilterChannelsByTeam(t *testing.T) {
	channels := []string{
		"#general",
		"#fleet",
		"#team-alpha-tasks",
		"#team-alpha-chat",
		"#team-bravo-tasks",
		"#ops",
	}

	tests := []struct {
		name    string
		keyTeam string
		wantChs []string
	}{
		{
			name:    "unrestricted: all channels",
			keyTeam: "",
			wantChs: []string{"#general", "#fleet", "#team-alpha-tasks", "#team-alpha-chat", "#team-bravo-tasks", "#ops"},
		},
		{
			name:    "team alpha: own team + non-team channels",
			keyTeam: "alpha",
			wantChs: []string{"#general", "#fleet", "#team-alpha-tasks", "#team-alpha-chat", "#ops"},
		},
		{
			name:    "team bravo: own team + non-team channels",
			keyTeam: "bravo",
			wantChs: []string{"#general", "#fleet", "#team-bravo-tasks", "#ops"},
		},
		{
			name:    "team ghost: only non-team channels",
			keyTeam: "ghost",
			wantChs: []string{"#general", "#fleet", "#ops"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterChannelsByTeam(channels, tt.keyTeam, nil)
			if len(got) != len(tt.wantChs) {
				t.Fatalf("got %d channels %v, want %d %v", len(got), got, len(tt.wantChs), tt.wantChs)
			}
			for i, ch := range got {
				if ch != tt.wantChs[i] {
					t.Errorf("channel[%d]: got %q, want %q", i, ch, tt.wantChs[i])
				}
			}
		})
	}
}
