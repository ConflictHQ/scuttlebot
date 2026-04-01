package sessionrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lrstanley/girc"
)

type ircConnector struct {
	http          *http.Client
	apiURL        string
	token         string
	channel       string
	nick          string
	addr          string
	agentType     string
	pass          string
	deleteOnClose bool

	mu       sync.RWMutex
	messages []Message
	client   *girc.Client
	errCh    chan error

	registeredByRelay bool
}

func newIRCConnector(cfg Config) (Connector, error) {
	if cfg.IRC.Addr == "" {
		return nil, fmt.Errorf("sessionrelay: irc transport requires irc addr")
	}
	return &ircConnector{
		http:          cfg.HTTPClient,
		apiURL:        stringsTrimRightSlash(cfg.URL),
		token:         cfg.Token,
		channel:       normalizeChannel(cfg.Channel),
		nick:          cfg.Nick,
		addr:          cfg.IRC.Addr,
		agentType:     cfg.IRC.AgentType,
		pass:          cfg.IRC.Pass,
		deleteOnClose: cfg.IRC.DeleteOnClose,
		messages:      make([]Message, 0, defaultBufferSize),
		errCh:         make(chan error, 1),
	}, nil
}

func (c *ircConnector) Connect(ctx context.Context) error {
	if err := c.ensureCredentials(ctx); err != nil {
		return err
	}

	host, port, err := splitHostPort(c.addr)
	if err != nil {
		return err
	}

	joined := make(chan struct{})
	var joinOnce sync.Once
	client := girc.New(girc.Config{
		Server: host,
		Port:   port,
		Nick:   c.nick,
		User:   c.nick,
		Name:   c.nick + " (session relay)",
		SASL:   &girc.SASLPlain{User: c.nick, Pass: c.pass},
	})
	client.Handlers.AddBg(girc.CONNECTED, func(cl *girc.Client, _ girc.Event) {
		cl.Cmd.Join(c.channel)
	})
	client.Handlers.AddBg(girc.JOIN, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil || e.Source.Name != c.nick {
			return
		}
		if normalizeChannel(e.Params[0]) != c.channel {
			return
		}
		joinOnce.Do(func() { close(joined) })
	})
	client.Handlers.AddBg(girc.PRIVMSG, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil {
			return
		}
		target := normalizeChannel(e.Params[0])
		if target != c.channel {
			return
		}
		sender := e.Source.Name
		text := strings.TrimSpace(e.Last())
		if sender == "bridge" && strings.HasPrefix(text, "[") {
			if end := strings.Index(text, "] "); end != -1 {
				sender = text[1:end]
				text = strings.TrimSpace(text[end+2:])
			}
		}
		c.appendMessage(Message{At: time.Now(), Nick: sender, Text: text})
	})

	c.client = client
	go func() {
		if err := client.Connect(); err != nil && ctx.Err() == nil {
			select {
			case c.errCh <- err:
			default:
			}
		}
	}()

	select {
	case <-ctx.Done():
		client.Close()
		return ctx.Err()
	case err := <-c.errCh:
		_ = c.cleanupRegistration(context.Background())
		return fmt.Errorf("sessionrelay: irc connect: %w", err)
	case <-joined:
		return nil
	}
}

func (c *ircConnector) Post(_ context.Context, text string) error {
	if c.client == nil {
		return fmt.Errorf("sessionrelay: irc client not connected")
	}
	c.client.Cmd.Message(c.channel, text)
	return nil
}

func (c *ircConnector) MessagesSince(_ context.Context, since time.Time) ([]Message, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]Message, 0, len(c.messages))
	for _, msg := range c.messages {
		if msg.At.After(since) {
			out = append(out, msg)
		}
	}
	return out, nil
}

func (c *ircConnector) Touch(context.Context) error {
	return nil
}

func (c *ircConnector) Close(ctx context.Context) error {
	if c.client != nil {
		c.client.Close()
	}
	return c.cleanupRegistration(ctx)
}

func (c *ircConnector) appendMessage(msg Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.messages) == defaultBufferSize {
		copy(c.messages, c.messages[1:])
		c.messages = c.messages[:defaultBufferSize-1]
	}
	c.messages = append(c.messages, msg)
}

func (c *ircConnector) ensureCredentials(ctx context.Context) error {
	if c.pass != "" {
		return nil
	}
	if c.apiURL == "" || c.token == "" {
		return fmt.Errorf("sessionrelay: irc transport requires irc pass or api url/token for auto-registration")
	}

	created, pass, err := c.registerOrRotate(ctx)
	if err != nil {
		return err
	}
	c.pass = pass
	c.registeredByRelay = created
	return nil
}

func (c *ircConnector) registerOrRotate(ctx context.Context) (bool, string, error) {
	body, _ := json.Marshal(map[string]any{
		"nick":     c.nick,
		"type":     c.agentType,
		"channels": []string{c.channel},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+"/v1/agents/register", bytes.NewReader(body))
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()

	var createdPayload struct {
		Credentials struct {
			Passphrase string `json:"passphrase"`
		} `json:"credentials"`
	}
	if resp.StatusCode == http.StatusCreated {
		if err := json.NewDecoder(resp.Body).Decode(&createdPayload); err != nil {
			return false, "", err
		}
		if createdPayload.Credentials.Passphrase == "" {
			return false, "", fmt.Errorf("sessionrelay: register %s: empty passphrase", c.nick)
		}
		return true, createdPayload.Credentials.Passphrase, nil
	}
	if resp.StatusCode != http.StatusConflict {
		return false, "", fmt.Errorf("sessionrelay: register %s: %s", c.nick, resp.Status)
	}

	rotateReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+"/v1/agents/"+c.nick+"/rotate", nil)
	if err != nil {
		return false, "", err
	}
	rotateReq.Header.Set("Authorization", "Bearer "+c.token)
	rotateResp, err := c.http.Do(rotateReq)
	if err != nil {
		return false, "", err
	}
	defer rotateResp.Body.Close()
	if rotateResp.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("sessionrelay: rotate %s: %s", c.nick, rotateResp.Status)
	}

	var rotated struct {
		Passphrase string `json:"passphrase"`
	}
	if err := json.NewDecoder(rotateResp.Body).Decode(&rotated); err != nil {
		return false, "", err
	}
	if rotated.Passphrase == "" {
		return false, "", fmt.Errorf("sessionrelay: rotate %s: empty passphrase", c.nick)
	}
	return false, rotated.Passphrase, nil
}

func (c *ircConnector) cleanupRegistration(ctx context.Context) error {
	if !c.deleteOnClose || !c.registeredByRelay || c.apiURL == "" || c.token == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.apiURL+"/v1/agents/"+c.nick, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("sessionrelay: delete %s: %s", c.nick, resp.Status)
	}
	c.registeredByRelay = false
	return nil
}

func splitHostPort(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("sessionrelay: invalid irc address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("sessionrelay: invalid irc port in %q: %w", addr, err)
	}
	return host, port, nil
}
