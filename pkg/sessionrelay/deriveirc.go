package sessionrelay

import (
	"net/url"
	"strings"
)

// DeriveIRCDefaults returns IRC transport defaults inferred from a scuttlebot
// API URL. Localhost gets plaintext on 6667; everything else gets TLS on 6697.
// Returns ("", false) when the URL is empty or unparseable so callers can
// distinguish "no derivation possible" from a deliberate plaintext default.
func DeriveIRCDefaults(apiURL string) (addr string, tls bool, ok bool) {
	if apiURL == "" {
		return "", false, false
	}
	u, err := url.Parse(apiURL)
	if err != nil || u.Host == "" {
		return "", false, false
	}
	host := u.Hostname()
	if host == "" {
		return "", false, false
	}
	if isLocalHost(host) {
		return host + ":6667", false, true
	}
	return host + ":6697", true, true
}

// IRCHostMatchesURL reports whether the host portion of ircAddr (host:port)
// matches the host portion of apiURL. Used to detect stale env-file values
// pointing at a different host than the configured URL — the symptom in #161.
// All flavors of localhost (localhost / 127.0.0.1 / ::1 / 0.0.0.0) compare
// equal so loopback aliases don't trip a false-mismatch warning.
func IRCHostMatchesURL(ircAddr, apiURL string) bool {
	if ircAddr == "" || apiURL == "" {
		return true // no signal — caller treats as "match"
	}
	u, err := url.Parse(apiURL)
	if err != nil || u.Host == "" {
		return true
	}
	urlHost := u.Hostname()
	if urlHost == "" {
		return true
	}
	addrHost := ircAddr
	if i := strings.LastIndex(addrHost, ":"); i >= 0 {
		addrHost = addrHost[:i]
	}
	if isLocalHost(addrHost) && isLocalHost(urlHost) {
		return true
	}
	return strings.EqualFold(addrHost, urlHost)
}

func isLocalHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	}
	return false
}
