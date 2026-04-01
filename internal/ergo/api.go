package ergo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// APIClient is an HTTP client for Ergo's management API.
type APIClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewAPIClient returns a new APIClient pointed at addr with the given bearer token.
func NewAPIClient(addr, token string) *APIClient {
	return &APIClient{
		baseURL: "http://" + addr,
		token:   token,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Status returns the Ergo server status.
func (c *APIClient) Status() (*StatusResponse, error) {
	var resp StatusResponse
	if err := c.post("/v1/status", nil, &resp); err != nil {
		return nil, fmt.Errorf("ergo api: status: %w", err)
	}
	return &resp, nil
}

// Rehash reloads Ergo's configuration file.
func (c *APIClient) Rehash() error {
	var resp successResponse
	if err := c.post("/v1/rehash", nil, &resp); err != nil {
		return fmt.Errorf("ergo api: rehash: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("ergo api: rehash failed: %s", resp.Error)
	}
	return nil
}

// RegisterAccount creates a NickServ account via saregister.
func (c *APIClient) RegisterAccount(name, passphrase string) error {
	var resp registerResponse
	if err := c.post("/v1/ns/saregister", map[string]string{
		"accountName": name,
		"passphrase":  passphrase,
	}, &resp); err != nil {
		return fmt.Errorf("ergo api: register account %q: %w", name, err)
	}
	if !resp.Success {
		return fmt.Errorf("ergo api: register account %q: %s", name, resp.ErrorCode)
	}
	return nil
}

// ChangePassword updates the passphrase of an existing NickServ account.
func (c *APIClient) ChangePassword(name, passphrase string) error {
	var resp passwdResponse
	if err := c.post("/v1/ns/passwd", map[string]string{
		"accountName": name,
		"passphrase":  passphrase,
	}, &resp); err != nil {
		return fmt.Errorf("ergo api: change password %q: %w", name, err)
	}
	if !resp.Success {
		return fmt.Errorf("ergo api: change password %q: %s", name, resp.ErrorCode)
	}
	return nil
}

// AccountInfo fetches details about a NickServ account.
func (c *APIClient) AccountInfo(name string) (*AccountInfoResponse, error) {
	var resp AccountInfoResponse
	if err := c.post("/v1/ns/info", map[string]string{
		"accountName": name,
	}, &resp); err != nil {
		return nil, fmt.Errorf("ergo api: account info %q: %w", name, err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("ergo api: account %q not found", name)
	}
	return &resp, nil
}

// ListChannels returns all channels currently active on the server.
func (c *APIClient) ListChannels() (*ListChannelsResponse, error) {
	var resp ListChannelsResponse
	if err := c.post("/v1/list", nil, &resp); err != nil {
		return nil, fmt.Errorf("ergo api: list channels: %w", err)
	}
	return &resp, nil
}

func (c *APIClient) post(path string, body any, out any) error {
	var reqBody bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&reqBody).Encode(body); err != nil {
			return err
		}
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, &reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Response types.

type successResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type registerResponse struct {
	Success   bool   `json:"success"`
	ErrorCode string `json:"errorCode,omitempty"`
	Error     string `json:"error,omitempty"`
}

type passwdResponse struct {
	Success   bool   `json:"success"`
	ErrorCode string `json:"errorCode,omitempty"`
}

// StatusResponse is the response from /v1/status.
type StatusResponse struct {
	Success   bool   `json:"success"`
	Version   string `json:"version"`
	StartTime string `json:"start_time"`
	Users     struct {
		Total int `json:"total"`
		Max   int `json:"max"`
	} `json:"users"`
	Channels int `json:"channels"`
}

// AccountInfoResponse is the response from /v1/ns/info.
type AccountInfoResponse struct {
	Success      bool     `json:"success"`
	AccountName  string   `json:"accountName"`
	Email        string   `json:"email"`
	RegisteredAt string   `json:"registeredAt"`
	Channels     []string `json:"channels"`
}

// ChannelInfo is a single channel entry from /v1/list.
type ChannelInfo struct {
	Name       string `json:"name"`
	UserCount  int    `json:"userCount"`
	Topic      string `json:"topic"`
	Registered bool   `json:"registered"`
}

// ListChannelsResponse is the response from /v1/list.
type ListChannelsResponse struct {
	Success  bool          `json:"success"`
	Channels []ChannelInfo `json:"channels"`
}
