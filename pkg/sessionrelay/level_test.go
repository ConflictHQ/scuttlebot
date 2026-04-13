package sessionrelay

import "testing"

func TestResolutionAccepts(t *testing.T) {
	tests := []struct {
		res    Resolution
		level  Level
		accept bool
	}{
		// ResMinimal accepts heartbeat and lifecycle only.
		{ResMinimal, LevelHeartbeat, true},
		{ResMinimal, LevelLifecycle, true},
		{ResMinimal, LevelAction, false},
		{ResMinimal, LevelContent, false},
		{ResMinimal, LevelReasoning, false},

		// ResActions adds action-level messages.
		{ResActions, LevelHeartbeat, true},
		{ResActions, LevelLifecycle, true},
		{ResActions, LevelAction, true},
		{ResActions, LevelContent, false},
		{ResActions, LevelReasoning, false},

		// ResFull adds content text.
		{ResFull, LevelHeartbeat, true},
		{ResFull, LevelLifecycle, true},
		{ResFull, LevelAction, true},
		{ResFull, LevelContent, true},
		{ResFull, LevelReasoning, false},

		// ResDebug accepts everything.
		{ResDebug, LevelHeartbeat, true},
		{ResDebug, LevelLifecycle, true},
		{ResDebug, LevelAction, true},
		{ResDebug, LevelContent, true},
		{ResDebug, LevelReasoning, true},
	}

	for _, tt := range tests {
		got := tt.res.Accepts(tt.level)
		if got != tt.accept {
			t.Errorf("%s.Accepts(%d) = %v, want %v", tt.res, tt.level, got, tt.accept)
		}
	}
}

func TestParseResolution(t *testing.T) {
	tests := []struct {
		input string
		want  Resolution
		err   bool
	}{
		{"minimal", ResMinimal, false},
		{"MINIMAL", ResMinimal, false},
		{" Minimal ", ResMinimal, false},
		{"actions", ResActions, false},
		{"ACTIONS", ResActions, false},
		{"full", ResFull, false},
		{"Full", ResFull, false},
		{"debug", ResDebug, false},
		{"DEBUG", ResDebug, false},
		{"", 0, true},
		{"unknown", 0, true},
		{"verbose", 0, true},
	}

	for _, tt := range tests {
		got, err := ParseResolution(tt.input)
		if (err != nil) != tt.err {
			t.Errorf("ParseResolution(%q) error = %v, wantErr %v", tt.input, err, tt.err)
			continue
		}
		if err == nil && got != tt.want {
			t.Errorf("ParseResolution(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestResolutionString(t *testing.T) {
	tests := []struct {
		res  Resolution
		want string
	}{
		{ResMinimal, "minimal"},
		{ResActions, "actions"},
		{ResFull, "full"},
		{ResDebug, "debug"},
		{Resolution(99), "Resolution(99)"},
	}

	for _, tt := range tests {
		if got := tt.res.String(); got != tt.want {
			t.Errorf("Resolution(%d).String() = %q, want %q", int(tt.res), got, tt.want)
		}
	}
}

func TestParseChannelResolutions(t *testing.T) {
	tests := []struct {
		input string
		want  map[string]Resolution
		err   bool
	}{
		{"", nil, false},
		{"#general:full", map[string]Resolution{"#general": ResFull}, false},
		{"general:full", map[string]Resolution{"#general": ResFull}, false},
		{
			"#general:full,#session-abc:debug,#project-foo:actions",
			map[string]Resolution{
				"#general":     ResFull,
				"#session-abc": ResDebug,
				"#project-foo": ResActions,
			},
			false,
		},
		{" #general : full , #team-x : minimal ", map[string]Resolution{
			"#general": ResFull,
			"#team-x":  ResMinimal,
		}, false},
		{"#general:badlevel", nil, true},
		{"nocolon", nil, true},
		{",,,", nil, false}, // empty pairs are skipped
	}

	for _, tt := range tests {
		got, err := ParseChannelResolutions(tt.input)
		if (err != nil) != tt.err {
			t.Errorf("ParseChannelResolutions(%q) error = %v, wantErr %v", tt.input, err, tt.err)
			continue
		}
		if err != nil {
			continue
		}
		if len(got) != len(tt.want) {
			t.Errorf("ParseChannelResolutions(%q) len = %d, want %d", tt.input, len(got), len(tt.want))
			continue
		}
		for k, v := range tt.want {
			if got[k] != v {
				t.Errorf("ParseChannelResolutions(%q)[%q] = %v, want %v", tt.input, k, got[k], v)
			}
		}
	}
}
