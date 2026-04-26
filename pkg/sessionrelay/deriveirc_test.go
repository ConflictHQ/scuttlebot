package sessionrelay

import "testing"

func TestDeriveIRCDefaults(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantOk  bool
		wantAdd string
		wantTLS bool
	}{
		{"empty", "", false, "", false},
		{"garbage", "::not-a-url", false, "", false},
		{"http localhost", "http://localhost:8080", true, "localhost:6667", false},
		{"http loopback", "http://127.0.0.1:8080", true, "127.0.0.1:6667", false},
		{"https prod", "https://irc.scuttlebot.net", true, "irc.scuttlebot.net:6697", true},
		{"http remote", "http://chat.example.com:8080", true, "chat.example.com:6697", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAdd, gotTLS, ok := DeriveIRCDefaults(tt.url)
			if ok != tt.wantOk {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOk)
			}
			if !ok {
				return
			}
			if gotAdd != tt.wantAdd {
				t.Errorf("addr = %q, want %q", gotAdd, tt.wantAdd)
			}
			if gotTLS != tt.wantTLS {
				t.Errorf("tls = %v, want %v", gotTLS, tt.wantTLS)
			}
		})
	}
}

func TestIRCHostMatchesURL(t *testing.T) {
	tests := []struct {
		name string
		addr string
		url  string
		want bool
	}{
		{"both empty", "", "", true},
		{"empty url", "127.0.0.1:6667", "", true},
		{"empty addr", "", "http://x", true},
		{"identical host", "irc.example.com:6697", "https://irc.example.com", true},
		{"loopback aliases", "127.0.0.1:6667", "http://localhost:8080", true},
		{"loopback v6", "::1:6667", "http://127.0.0.1:8080", true},
		{"prod vs local mismatch", "irc.scuttlebot.net:6697", "http://localhost:8080", false},
		{"different host", "chat.example.com:6697", "https://irc.example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IRCHostMatchesURL(tt.addr, tt.url)
			if got != tt.want {
				t.Errorf("IRCHostMatchesURL(%q, %q) = %v, want %v", tt.addr, tt.url, got, tt.want)
			}
		})
	}
}
