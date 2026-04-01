package sessionrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"
)

type httpConnector struct {
	http    *http.Client
	baseURL string
	token   string
	primary string
	nick    string

	mu       sync.RWMutex
	channels []string
}

type httpMessage struct {
	At   string `json:"at"`
	Nick string `json:"nick"`
	Text string `json:"text"`
}

func newHTTPConnector(cfg Config) Connector {
	return &httpConnector{
		http:     cfg.HTTPClient,
		baseURL:  stringsTrimRightSlash(cfg.URL),
		token:    cfg.Token,
		primary:  normalizeChannel(cfg.Channel),
		nick:     cfg.Nick,
		channels: append([]string(nil), cfg.Channels...),
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
	for _, channel := range c.Channels() {
		if err := c.PostTo(ctx, channel, text); err != nil {
			return err
		}
	}
	return nil
}

func (c *httpConnector) PostTo(ctx context.Context, channel, text string) error {
	channel = channelSlug(channel)
	if channel == "" {
		return fmt.Errorf("sessionrelay: post channel is required")
	}
	return c.postJSON(ctx, "/v1/channels/"+channel+"/messages", map[string]string{
		"nick": c.nick,
		"text": text,
	})
}

func (c *httpConnector) MessagesSince(ctx context.Context, since time.Time) ([]Message, error) {
	out := make([]Message, 0, 32)
	for _, channel := range c.Channels() {
		url := c.baseURL + "/v1/channels/" + channelSlug(channel) + "/messages"
		if !since.IsZero() {
			url += "?since=" + since.UTC().Format(time.RFC3339Nano)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		c.authorize(req)

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 != 2 {
			resp.Body.Close()
			return nil, fmt.Errorf("sessionrelay: http messages: %s", resp.Status)
		}

		var payload struct {
			Messages []httpMessage `json:"messages"`
		}
		err = json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		for _, msg := range payload.Messages {
			at, err := time.Parse(time.RFC3339Nano, msg.At)
			if err != nil {
				continue
			}
			out = append(out, Message{At: at, Channel: channel, Nick: msg.Nick, Text: msg.Text})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

func (c *httpConnector) Touch(ctx context.Context) error {
	for _, channel := range c.Channels() {
		err := c.postJSON(ctx, "/v1/channels/"+channelSlug(channel)+"/presence", map[string]string{"nick": c.nick})
		if err == nil {
			continue
		}
		var statusErr *statusError
		if errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusNotFound || statusErr.StatusCode == http.StatusMethodNotAllowed) {
			continue
		}
		return err
	}
	return nil
}

func (c *httpConnector) JoinChannel(_ context.Context, channel string) error {
	channel = normalizeChannel(channel)
	if channel == "" {
		return fmt.Errorf("sessionrelay: join channel is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if slices.Contains(c.channels, channel) {
		return nil
	}
	c.channels = append(c.channels, channel)
	return nil
}

func (c *httpConnector) PartChannel(_ context.Context, channel string) error {
	channel = normalizeChannel(channel)
	if channel == "" {
		return fmt.Errorf("sessionrelay: part channel is required")
	}
	if channel == c.primary {
		return fmt.Errorf("sessionrelay: cannot part control channel %s", channel)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	filtered := c.channels[:0]
	for _, existing := range c.channels {
		if existing == channel {
			continue
		}
		filtered = append(filtered, existing)
	}
	c.channels = filtered
	return nil
}

func (c *httpConnector) Channels() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.channels...)
}

func (c *httpConnector) ControlChannel() string {
	return c.primary
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
