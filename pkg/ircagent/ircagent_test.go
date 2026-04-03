package ircagent

import "testing"

func TestMentionsNick(t *testing.T) {
	t.Helper()

	tests := []struct {
		name string
		text string
		nick string
		want bool
	}{
		{name: "simple mention", text: "codex: hello", nick: "codex", want: true},
		{name: "mention in sentence", text: "hey codex can you help", nick: "codex", want: true},
		{name: "path does not trigger", text: "look at .claude/hooks/settings.json", nick: "claude", want: false},
		{name: "windows path does not trigger", text: `check C:\Users\me\.codex\hooks`, nick: "codex", want: false},
		{name: "substring does not trigger", text: "codexagent is a process", nick: "codex", want: false},
		{name: "hyphen boundary does not trigger", text: "claude-scuttlebot-a1b2c3d4 posted status", nick: "claude", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MentionsNick(tt.text, tt.nick)
			if got != tt.want {
				t.Fatalf("MentionsNick(%q, %q) = %v, want %v", tt.text, tt.nick, got, tt.want)
			}
		})
	}
}

func TestSplitCSV(t *testing.T) {
	t.Helper()

	got := SplitCSV(" #general, #fleet ,, #agent.codex ")
	want := []string{"#general", "#fleet", "#agent.codex"}
	if len(got) != len(want) {
		t.Fatalf("len(SplitCSV) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SplitCSV()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestHasAnyPrefix(t *testing.T) {
	t.Helper()

	prefixes := []string{"claude-", "codex-", "gemini-"}
	if !HasAnyPrefix("claude-abc", prefixes) {
		t.Fatalf("expected claude prefix match")
	}
	if !HasAnyPrefix("codex-1234", prefixes) {
		t.Fatalf("expected codex prefix match")
	}
	if !HasAnyPrefix("gemini-1234", prefixes) {
		t.Fatalf("expected gemini prefix match")
	}
	if HasAnyPrefix("glengoolie", prefixes) {
		t.Fatalf("did not expect non-activity sender to match")
	}
}

func TestTrimAddressedText(t *testing.T) {
	t.Helper()

	tests := []struct {
		name string
		text string
		nick string
		want string
	}{
		{name: "colon address", text: "codex-scuttlebot-1234: read README.md", nick: "codex-scuttlebot-1234", want: "read README.md"},
		{name: "comma address", text: "codex, status?", nick: "codex", want: "status?"},
		{name: "plain text stays", text: "hello there", nick: "codex", want: "hello there"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TrimAddressedText(tt.text, tt.nick)
			if got != tt.want {
				t.Fatalf("TrimAddressedText(%q, %q) = %q, want %q", tt.text, tt.nick, got, tt.want)
			}
		})
	}
}

func TestMatchesGroupMention(t *testing.T) {
	tests := []struct {
		name, text, nick, agentType string
		want                        bool
	}{
		{"@all matches everyone", "@all stop working", "claude-kohakku-abc", "worker", true},
		{"@all mid-sentence", "hey @all check this", "gemini-foo-123", "worker", true},
		{"@worker matches worker type", "@worker report status", "claude-kohakku-abc", "worker", true},
		{"@worker doesn't match observer", "@worker report", "obs-bot", "observer", false},
		{"@observer matches observer", "@observer watch this", "obs-bot", "observer", true},
		{"@claude-* matches claude agents", "@claude-* pause", "claude-kohakku-abc", "worker", true},
		{"@claude-* doesn't match gemini", "@claude-* pause", "gemini-kohakku-abc", "worker", false},
		{"@claude-kohakku-* matches specific", "@claude-kohakku-* stop", "claude-kohakku-abc", "worker", true},
		{"@gemini-* matches gemini", "@gemini-* summarize", "gemini-proj-123", "worker", true},
		{"no mention no match", "hello world", "claude-abc", "worker", false},
		{"partial @all no match", "install @alloy", "claude-abc", "worker", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchesGroupMention(tt.text, tt.nick, tt.agentType)
			if got != tt.want {
				t.Errorf("MatchesGroupMention(%q, %q, %q) = %v, want %v", tt.text, tt.nick, tt.agentType, got, tt.want)
			}
		})
	}
}
