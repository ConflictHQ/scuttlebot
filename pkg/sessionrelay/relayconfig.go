package sessionrelay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// RelayConfig mirrors the JSON shape returned by GET /v1/relay/config on the
// daemon. It carries policy values relays should apply at runtime so admin-UI
// changes don't require an env restart.
type RelayConfig struct {
	ChannelResolutions map[string]string `json:"channel_resolutions"`
	HandoffBudget      int               `json:"handoff_budget,omitempty"`
}

// FetchRelayConfig issues a GET against /v1/relay/config and returns the
// decoded payload. If the daemon doesn't expose the endpoint (older build) or
// the API URL/token aren't set, returns a zero RelayConfig and a non-fatal
// error so callers can fall back to env-derived defaults.
func FetchRelayConfig(ctx context.Context, apiURL, token string) (RelayConfig, error) {
	var out RelayConfig
	if apiURL == "" || token == "" {
		return out, fmt.Errorf("sessionrelay: relay config fetch requires url + token")
	}
	url := strings.TrimRight(apiURL, "/") + "/v1/relay/config"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// Daemon doesn't have the endpoint — pre-v1.5 build. Caller falls back.
		return out, fmt.Errorf("sessionrelay: relay config endpoint not available")
	}
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("sessionrelay: relay config: unexpected status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("sessionrelay: relay config: decode: %w", err)
	}
	return out, nil
}

// ApplyChannelResolutions parses each "level" string against ParseResolution
// and calls SetResolution on the FilteredConnector for every channel where
// the level is recognised. Unknown levels are skipped silently. Channel names
// are normalized (leading "#" added if missing) so admin-UI input either form
// works the same.
func ApplyChannelResolutions(filtered *FilteredConnector, resolutions map[string]string) {
	if filtered == nil || len(resolutions) == 0 {
		return
	}
	for ch, lvl := range resolutions {
		res, err := ParseResolution(lvl)
		if err != nil {
			continue
		}
		filtered.SetResolution(ch, res)
	}
}
