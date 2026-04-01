package sessionrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type httpConnector struct {
	http    *http.Client
	baseURL string
	token   string
	channel string
	nick    string
}

type httpMessage struct {
	At   string `json:"at"`
	Nick string `json:"nick"`
	Text string `json:"text"`
}

func newHTTPConnector(cfg Config) Connector {
	return &httpConnector{
		http:    cfg.HTTPClient,
		baseURL: stringsTrimRightSlash(cfg.URL),
		token:   cfg.Token,
		channel: channelSlug(cfg.Channel),
		nick:    cfg.Nick,
	}
}

func (c *httpConnector) Connect(context.Context) error {
	if c.baseURL == "" {
		return fmt.Errorf("sessionrelay: http transport requires url")
	}
	if c.token == "" {
		return fmt.Errorf("sessionrelay: http transport requires token")
	}
	return nil
}

func (c *httpConnector) Post(ctx context.Context, text string) error {
	return c.postJSON(ctx, "/v1/channels/"+c.channel+"/messages", map[string]string{
		"nick": c.nick,
		"text": text,
	})
}

func (c *httpConnector) MessagesSince(ctx context.Context, since time.Time) ([]Message, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/channels/"+c.channel+"/messages", nil)
	if err != nil {
		return nil, err
	}
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("sessionrelay: http messages: %s", resp.Status)
	}

	var payload struct {
		Messages []httpMessage `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	out := make([]Message, 0, len(payload.Messages))
	for _, msg := range payload.Messages {
		at, err := time.Parse(time.RFC3339Nano, msg.At)
		if err != nil {
			continue
		}
		if !at.After(since) {
			continue
		}
		out = append(out, Message{At: at, Nick: msg.Nick, Text: msg.Text})
	}
	return out, nil
}

func (c *httpConnector) Touch(ctx context.Context) error {
	err := c.postJSON(ctx, "/v1/channels/"+c.channel+"/presence", map[string]string{"nick": c.nick})
	if err == nil {
		return nil
	}
	var statusErr *statusError
	if errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusNotFound || statusErr.StatusCode == http.StatusMethodNotAllowed) {
		return nil
	}
	return err
}

func (c *httpConnector) Close(context.Context) error {
	return nil
}

func (c *httpConnector) postJSON(ctx context.Context, path string, body any) error {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	c.authorize(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return &statusError{Op: path, StatusCode: resp.StatusCode, Status: resp.Status}
	}
	return nil
}

func (c *httpConnector) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
}

type statusError struct {
	Op         string
	StatusCode int
	Status     string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("sessionrelay: %s: %s", e.Op, e.Status)
}

func stringsTrimRightSlash(value string) string {
	for len(value) > 0 && value[len(value)-1] == '/' {
		value = value[:len(value)-1]
	}
	return value
}
