package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ChannelInfo is the result of creating or looking up a channel.
type ChannelInfo struct {
	// Channel is the full IRC channel name (e.g. "#task.gh-42").
	Channel string `json:"channel"`

	// Type is the channel type name from the topology policy (e.g. "task", "sprint").
	// Empty if the channel does not match any configured type.
	Type string `json:"type,omitempty"`

	// Supervision is the coordination/supervision channel where summaries
	// from this channel should also be posted (e.g. "#general"). Empty if none.
	Supervision string `json:"supervision,omitempty"`

	// Autojoin is the list of bot nicks that were invited when the channel was created.
	Autojoin []string `json:"autojoin,omitempty"`
}

// ChannelTypeInfo describes a class of channels defined in the topology policy.
type ChannelTypeInfo struct {
	// Name is the type identifier (e.g. "task", "sprint").
	Name string `json:"name"`

	// Prefix is the channel name prefix (e.g. "task.").
	Prefix string `json:"prefix"`

	// Autojoin is the list of bot nicks invited when a channel of this type is created.
	Autojoin []string `json:"autojoin,omitempty"`

	// Supervision is the coordination channel for this type, or empty.
	Supervision string `json:"supervision,omitempty"`

	// Ephemeral indicates channels of this type are automatically reaped.
	Ephemeral bool `json:"ephemeral,omitempty"`

	// TTLSeconds is the maximum lifetime in seconds for ephemeral channels, or zero.
	TTLSeconds int64 `json:"ttl_seconds,omitempty"`
}

// TopologyClient calls the scuttlebot HTTP API to provision and discover channels.
// It complements the IRC-based Client for the dual-channel pattern: agents create
// a task channel here and get back the supervision channel where they should also post.
type TopologyClient struct {
	apiURL string
	token  string
	http   *http.Client
}

// NewTopologyClient creates a TopologyClient.
// apiURL is the base URL of the scuttlebot API (e.g. "http://localhost:8080").
// token is the Bearer token issued by scuttlebot.
func NewTopologyClient(apiURL, token string) *TopologyClient {
	return &TopologyClient{
		apiURL: apiURL,
		token:  token,
		http:   &http.Client{},
	}
}

type createChannelReq struct {
	Name     string   `json:"name"`
	Topic    string   `json:"topic,omitempty"`
	Ops      []string `json:"ops,omitempty"`
	Voice    []string `json:"voice,omitempty"`
	Autojoin []string `json:"autojoin,omitempty"`
}

// CreateChannel provisions an IRC channel via the scuttlebot topology API.
// The server applies autojoin policy and invites the configured bots.
// Returns a ChannelInfo with the channel name, type, and supervision channel.
//
// Example: create a task channel for a GitHub issue.
//
//	info, err := topo.CreateChannel(ctx, "#task.gh-42", "GitHub issue #42")
//	if err != nil { ... }
//	// post activity to info.Channel, summaries to info.Supervision
func (t *TopologyClient) CreateChannel(ctx context.Context, name, topic string) (ChannelInfo, error) {
	body, err := json.Marshal(createChannelReq{Name: name, Topic: topic})
	if err != nil {
		return ChannelInfo{}, fmt.Errorf("topology: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.apiURL+"/v1/channels", bytes.NewReader(body))
	if err != nil {
		return ChannelInfo{}, fmt.Errorf("topology: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.token)

	resp, err := t.http.Do(req)
	if err != nil {
		return ChannelInfo{}, fmt.Errorf("topology: create channel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		var apiErr struct{ Error string `json:"error"` }
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		return ChannelInfo{}, fmt.Errorf("topology: create channel: %s", apiErr.Error)
	}
	var info ChannelInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return ChannelInfo{}, fmt.Errorf("topology: decode response: %w", err)
	}
	return info, nil
}

// DropChannel drops an ephemeral channel via the scuttlebot topology API.
// The ChanServ registration is removed and the channel will be vacated.
func (t *TopologyClient) DropChannel(ctx context.Context, channel string) error {
	if len(channel) < 2 || channel[0] != '#' {
		return fmt.Errorf("topology: invalid channel name %q", channel)
	}
	slug := channel[1:] // strip leading #
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.apiURL+"/v1/topology/channels/"+slug, nil)
	if err != nil {
		return fmt.Errorf("topology: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	resp, err := t.http.Do(req)
	if err != nil {
		return fmt.Errorf("topology: drop channel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		var apiErr struct{ Error string `json:"error"` }
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		return fmt.Errorf("topology: drop channel: %s", apiErr.Error)
	}
	return nil
}

type topologyResp struct {
	StaticChannels []string          `json:"static_channels"`
	Types          []ChannelTypeInfo `json:"types"`
}

// GetTopology returns the channel type rules and static channels from the server.
func (t *TopologyClient) GetTopology(ctx context.Context) ([]string, []ChannelTypeInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.apiURL+"/v1/topology", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("topology: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	resp, err := t.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("topology: get topology: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("topology: get topology: status %d", resp.StatusCode)
	}
	var body topologyResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, nil, fmt.Errorf("topology: decode response: %w", err)
	}
	return body.StaticChannels, body.Types, nil
}

// PostActivity sends a structured message to the task/activity channel.
// It is a convenience wrapper around client.Send for the dual-channel pattern.
func PostActivity(ctx context.Context, c *Client, channel, msgType string, payload any) error {
	return c.Send(ctx, channel, msgType, payload)
}

// PostSummary sends a structured message to the supervision channel.
// supervision is the channel returned by CreateChannel (info.Supervision).
// It is a no-op if supervision is empty.
func PostSummary(ctx context.Context, c *Client, supervision, msgType string, payload any) error {
	if supervision == "" {
		return nil
	}
	return c.Send(ctx, supervision, msgType, payload)
}
