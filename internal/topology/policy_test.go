package topology

import (
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/internal/config"
)

func testPolicy() *Policy {
	return NewPolicy(config.TopologyConfig{
		Channels: []config.StaticChannelConfig{
			{
				Name:     "#general",
				Topic:    "Fleet coordination",
				Autojoin: []string{"bridge", "oracle", "scribe"},
			},
			{
				Name:     "#alerts",
				Autojoin: []string{"bridge", "sentinel", "steward"},
			},
		},
		Types: []config.ChannelTypeConfig{
			{
				Name:        "task",
				Prefix:      "task.",
				Autojoin:    []string{"bridge", "scribe"},
				Supervision: "#general",
				Ephemeral:   true,
				TTL:         config.Duration{Duration: 72 * time.Hour},
			},
			{
				Name:        "sprint",
				Prefix:      "sprint.",
				Autojoin:    []string{"bridge", "oracle", "herald"},
				Supervision: "",
			},
			{
				Name:        "incident",
				Prefix:      "incident.",
				Autojoin:    []string{"bridge", "sentinel", "steward", "oracle"},
				Supervision: "#alerts",
				Ephemeral:   true,
				TTL:         config.Duration{Duration: 168 * time.Hour},
			},
			{
				Name:   "experiment",
				Prefix: "experiment.",
			},
		},
	})
}

func TestPolicyMatch(t *testing.T) {
	p := testPolicy()

	cases := []struct {
		channel  string
		wantType string
	}{
		{"#task.gh-42", "task"},
		{"#task.JIRA-99", "task"},
		{"#sprint.2026-q2", "sprint"},
		{"#incident.p1", "incident"},
		{"#experiment.llm-v3", "experiment"},
		{"#general", ""},      // static, no type
		{"#unknown", ""},      // no match
		{"#taskforce", ""},    // prefix must match exactly (task. not task)
	}

	for _, tc := range cases {
		t.Run(tc.channel, func(t *testing.T) {
			got := p.TypeName(tc.channel)
			if got != tc.wantType {
				t.Errorf("TypeName(%q) = %q, want %q", tc.channel, got, tc.wantType)
			}
		})
	}
}

func TestPolicyAutojoinFor(t *testing.T) {
	p := testPolicy()

	cases := []struct {
		channel  string
		wantBots []string
	}{
		{"#task.gh-42", []string{"bridge", "scribe"}},
		{"#sprint.2026-q2", []string{"bridge", "oracle", "herald"}},
		{"#incident.p1", []string{"bridge", "sentinel", "steward", "oracle"}},
		{"#general", []string{"bridge", "oracle", "scribe"}},   // static channel
		{"#alerts", []string{"bridge", "sentinel", "steward"}}, // static channel
		{"#unknown", nil},
		{"#experiment.llm-v3", nil}, // type exists but no autojoin configured
	}

	for _, tc := range cases {
		t.Run(tc.channel, func(t *testing.T) {
			got := p.AutojoinFor(tc.channel)
			if len(got) != len(tc.wantBots) {
				t.Fatalf("AutojoinFor(%q) = %v, want %v", tc.channel, got, tc.wantBots)
			}
			for i, nick := range tc.wantBots {
				if got[i] != nick {
					t.Errorf("AutojoinFor(%q)[%d] = %q, want %q", tc.channel, i, got[i], nick)
				}
			}
		})
	}
}

func TestPolicySupervisionFor(t *testing.T) {
	p := testPolicy()

	cases := []struct {
		channel string
		want    string
	}{
		{"#task.gh-42", "#general"},
		{"#incident.p1", "#alerts"},
		{"#sprint.2026-q2", ""},
		{"#general", ""},
		{"#unknown", ""},
	}

	for _, tc := range cases {
		t.Run(tc.channel, func(t *testing.T) {
			got := p.SupervisionFor(tc.channel)
			if got != tc.want {
				t.Errorf("SupervisionFor(%q) = %q, want %q", tc.channel, got, tc.want)
			}
		})
	}
}

func TestPolicyEphemeral(t *testing.T) {
	p := testPolicy()

	if !p.IsEphemeral("#task.gh-42") {
		t.Error("#task.gh-42 should be ephemeral")
	}
	if p.IsEphemeral("#sprint.2026-q2") {
		t.Error("#sprint.2026-q2 should not be ephemeral")
	}
	if p.IsEphemeral("#general") {
		t.Error("#general should not be ephemeral")
	}

	if got := p.TTLFor("#task.gh-42"); got != 72*time.Hour {
		t.Errorf("TTLFor #task.gh-42 = %v, want 72h", got)
	}
	if got := p.TTLFor("#incident.p1"); got != 168*time.Hour {
		t.Errorf("TTLFor #incident.p1 = %v, want 168h", got)
	}
	if got := p.TTLFor("#sprint.2026-q2"); got != 0 {
		t.Errorf("TTLFor #sprint.2026-q2 = %v, want 0", got)
	}
}

func TestPolicyStaticChannels(t *testing.T) {
	p := testPolicy()
	statics := p.StaticChannels()
	if len(statics) != 2 {
		t.Fatalf("want 2 static channels, got %d", len(statics))
	}
	if statics[0].Name != "#general" {
		t.Errorf("statics[0].Name = %q, want #general", statics[0].Name)
	}
}

func TestPolicyTypes(t *testing.T) {
	p := testPolicy()
	types := p.Types()
	if len(types) != 4 {
		t.Fatalf("want 4 types, got %d", len(types))
	}
}

func TestNewPolicyEmpty(t *testing.T) {
	p := NewPolicy(config.TopologyConfig{})
	if p.Match("#anything") != nil {
		t.Error("empty policy should not match")
	}
	if p.AutojoinFor("#general") != nil {
		t.Error("empty policy should return nil autojoin")
	}
}
