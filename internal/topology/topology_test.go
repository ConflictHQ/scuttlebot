package topology_test

import (
	"testing"

	"github.com/conflicthq/scuttlebot/internal/topology"
)

func TestValidateName(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		wantErr bool
	}{
		{"valid fleet", "#fleet", false},
		{"valid project", "#project.myapp", false},
		{"valid subtopic", "#project.myapp.tasks.backend", false},
		{"valid task", "#task.01HX123", false},
		{"missing prefix", "fleet", true},
		{"empty after prefix", "#", true},
		{"contains space", "#my channel", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := topology.ValidateName(tc.channel)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateName(%q): expected error, got nil", tc.channel)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateName(%q): unexpected error: %v", tc.channel, err)
			}
		})
	}
}
