// Package apiclient is a minimal HTTP client for the scuttlebot REST API.
package apiclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Client calls the scuttlebot REST API.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New creates a Client targeting baseURL (e.g. "http://localhost:8080") with
// the given bearer token.
func New(baseURL, token string) *Client {
	return &Client{base: baseURL, token: token, http: &http.Client{}}
}

// Status returns the raw JSON bytes from GET /v1/status.
func (c *Client) Status() (json.RawMessage, error) {
	return c.get("/v1/status")
}

// ListAgents returns the raw JSON bytes from GET /v1/agents.
func (c *Client) ListAgents() (json.RawMessage, error) {
	return c.get("/v1/agents")
}

// GetAgent returns the raw JSON bytes from GET /v1/agents/{nick}.
func (c *Client) GetAgent(nick string) (json.RawMessage, error) {
	return c.get("/v1/agents/" + nick)
}

// RegisterAgent sends POST /v1/agents/register and returns raw JSON.
func (c *Client) RegisterAgent(nick, agentType string, channels []string) (json.RawMessage, error) {
	body := map[string]any{"nick": nick}
	if agentType != "" {
		body["type"] = agentType
	}
	if len(channels) > 0 {
		body["channels"] = channels
	}
	return c.post("/v1/agents/register", body)
}

// RevokeAgent sends POST /v1/agents/{nick}/revoke.
func (c *Client) RevokeAgent(nick string) error {
	_, err := c.post("/v1/agents/"+nick+"/revoke", nil)
	return err
}

// RotateAgent sends POST /v1/agents/{nick}/rotate and returns raw JSON.
func (c *Client) RotateAgent(nick string) (json.RawMessage, error) {
	return c.post("/v1/agents/"+nick+"/rotate", nil)
}

func (c *Client) get(path string) (json.RawMessage, error) {
	return c.do("GET", path, nil)
}

func (c *Client) post(path string, body any) (json.RawMessage, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, err
		}
	}
	return c.do("POST", path, &buf)
}

func (c *Client) do(method, path string, body io.Reader) (json.RawMessage, error) {
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		// Try to extract error message from JSON body.
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
			return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, apiErr.Error)
		}
		return nil, fmt.Errorf("API error %d", resp.StatusCode)
	}

	if len(data) == 0 {
		return nil, nil
	}
	return json.RawMessage(data), nil
}
