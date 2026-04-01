// Package apiclient is a minimal HTTP client for the scuttlebot REST API.
package apiclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// DeleteAgent sends DELETE /v1/agents/{nick}.
func (c *Client) DeleteAgent(nick string) error {
	_, err := c.doNoBody("DELETE", "/v1/agents/"+nick)
	return err
}

// ChannelUsers sends GET /v1/channels/{channel}/users and returns raw JSON.
func (c *Client) ChannelUsers(channel string) (json.RawMessage, error) {
	return c.get("/v1/channels/" + channel + "/users")
}

// DeleteChannel sends DELETE /v1/channels/{channel}.
func (c *Client) DeleteChannel(channel string) error {
	channel = strings.TrimPrefix(channel, "#")
	_, err := c.doNoBody("DELETE", "/v1/channels/"+channel)
	return err
}

// ListChannels sends GET /v1/channels and returns raw JSON.
func (c *Client) ListChannels() (json.RawMessage, error) {
	return c.get("/v1/channels")
}

// ListLLMBackends sends GET /v1/llm/backends and returns raw JSON.
func (c *Client) ListLLMBackends() (json.RawMessage, error) {
	return c.get("/v1/llm/backends")
}

// GetLLMBackend sends GET /v1/llm/backends and finds the named backend, returning raw JSON.
func (c *Client) GetLLMBackend(name string) (json.RawMessage, error) {
	raw, err := c.get("/v1/llm/backends")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Backends []json.RawMessage `json:"backends"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	for _, b := range resp.Backends {
		var named struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(b, &named) == nil && named.Name == name {
			return b, nil
		}
	}
	return nil, fmt.Errorf("backend %q not found", name)
}

// CreateLLMBackend sends POST /v1/llm/backends.
func (c *Client) CreateLLMBackend(cfg map[string]any) error {
	_, err := c.post("/v1/llm/backends", cfg)
	return err
}

// DeleteLLMBackend sends DELETE /v1/llm/backends/{name}.
func (c *Client) DeleteLLMBackend(name string) error {
	_, err := c.doNoBody("DELETE", "/v1/llm/backends/"+name)
	return err
}

// ListAdmins sends GET /v1/admins and returns raw JSON.
func (c *Client) ListAdmins() (json.RawMessage, error) {
	return c.get("/v1/admins")
}

// AddAdmin sends POST /v1/admins and returns raw JSON.
func (c *Client) AddAdmin(username, password string) (json.RawMessage, error) {
	return c.post("/v1/admins", map[string]string{"username": username, "password": password})
}

// RemoveAdmin sends DELETE /v1/admins/{username}.
func (c *Client) RemoveAdmin(username string) error {
	_, err := c.doNoBody("DELETE", "/v1/admins/"+username)
	return err
}

// SetAdminPassword sends PUT /v1/admins/{username}/password.
func (c *Client) SetAdminPassword(username, password string) error {
	_, err := c.put("/v1/admins/"+username+"/password", map[string]string{"password": password})
	return err
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

func (c *Client) put(path string, body any) (json.RawMessage, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, err
		}
	}
	return c.do("PUT", path, &buf)
}

func (c *Client) doNoBody(method, path string) (json.RawMessage, error) {
	return c.do(method, path, nil)
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
