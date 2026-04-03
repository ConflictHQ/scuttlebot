package sessionrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
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
	primary       string
	nick          string
	addr          string
	agentType     string
	pass          string
	deleteOnClose bool

	mu       sync.RWMutex
	channels []string
	messages []Message
	client   *girc.Client
	errCh    chan error

	registeredByRelay bool
	connectedAt       time.Time
}

func newIRCConnector(cfg Config) (Connector, error) {
	if cfg.IRC.Addr == "" {
		return nil, fmt.Errorf("sessionrelay: irc transport requires irc addr")
	}
	return &ircConnector{
		http:          cfg.HTTPClient,
		apiURL:        stringsTrimRightSlash(cfg.URL),
		token:         cfg.Token,
		primary:       normalizeChannel(cfg.Channel),
		nick:          cfg.Nick,
		addr:          cfg.IRC.Addr,
		agentType:     cfg.IRC.AgentType,
		pass:          cfg.IRC.Pass,
		deleteOnClose: cfg.IRC.DeleteOnClose,
		channels:      append([]string(nil), cfg.Channels...),
		messages:      make([]Message, 0, defaultBufferSize),
		errCh:         make(chan error, 1),
	}, nil
}

const (
	ircReconnectMin = 2 * time.Second
	ircReconnectMax = 30 * time.Second
)

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
	c.dial(host, port, func() { joinOnce.Do(func() { close(joined) }) })

	select {
	case <-ctx.Done():
		c.mu.Lock()
		if c.client != nil {
			c.client.Close()
		}
		c.mu.Unlock()
		return ctx.Err()
	case err := <-c.errCh:
		_ = c.cleanupRegistration(context.Background())
		return fmt.Errorf("sessionrelay: irc connect: %w", err)
	case <-joined:
		go c.keepAlive(ctx, host, port)
		return nil
	}
}

// dial creates a fresh girc client, wires up handlers, and starts the
// connection goroutine. onJoined fires once when the primary channel is
// joined — used as the initial-connect signal and to reset backoff on
// successful reconnects.
func (c *ircConnector) dial(host string, port int, onJoined func()) {
	client := girc.New(girc.Config{
		Server:      host,
		Port:        port,
		Nick:        c.nick,
		User:        c.nick,
		Name:        c.nick + " (session relay)",
		SASL:        &girc.SASLPlain{User: c.nick, Pass: c.pass},
		PingDelay:   30 * time.Second,
		PingTimeout: 30 * time.Second,
	})
	client.Handlers.AddBg(girc.CONNECTED, func(cl *girc.Client, _ girc.Event) {
		c.mu.Lock()
		c.connectedAt = time.Now()
		c.mu.Unlock()
		for _, channel := range c.Channels() {
			cl.Cmd.Join(channel)
		}
	})
	client.Handlers.AddBg(girc.JOIN, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil || e.Source.Name != c.nick {
			return
		}
		if normalizeChannel(e.Params[0]) != c.primary {
			return
		}
		if onJoined != nil {
			onJoined()
		}
	})
	client.Handlers.AddBg(girc.PRIVMSG, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 || e.Source == nil {
			return
		}
		target := normalizeChannel(e.Params[0])
		if !c.hasChannel(target) {
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
		c.appendMessage(Message{At: time.Now(), Channel: target, Nick: sender, Text: text})
	})

	c.mu.Lock()
	c.client = client
	c.mu.Unlock()

	go func() {
		err := client.Connect()
		if err == nil {
			err = fmt.Errorf("connection closed")
		}
		select {
		case c.errCh <- err:
		default:
		}
	}()
}

// keepAlive watches for connection errors and redials with exponential backoff.
// It stops when ctx is cancelled (i.e. the broker is shutting down).
func (c *ircConnector) keepAlive(ctx context.Context, host string, port int) {
	wait := ircReconnectMin
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-c.errCh:
			fmt.Fprintf(os.Stderr, "sessionrelay: connection lost: %v\n", err)
		}

		// Close the dead client before replacing it.
		c.mu.Lock()
		if c.client != nil {
			c.client.Close()
			c.client = nil
		}
		c.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		fmt.Fprintf(os.Stderr, "sessionrelay: reconnecting (backoff %v)...\n", wait)

		// Re-register to get fresh SASL credentials in case the server
		// restarted and the Ergo database was reset.
		c.pass = "" // clear stale creds
		if err := c.ensureCredentials(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "sessionrelay: reconnect credential refresh failed: %v\n", err)
			wait = min(wait*2, ircReconnectMax)
			// Push a synthetic error so the loop retries.
			go func() {
				select {
				case c.errCh <- err:
				default:
				}
			}()
			continue
		}
		fmt.Fprintf(os.Stderr, "sessionrelay: credentials refreshed, dialing...\n")

		wait = min(wait*2, ircReconnectMax)
		c.dial(host, port, func() {
			wait = ircReconnectMin
			fmt.Fprintf(os.Stderr, "sessionrelay: reconnected successfully\n")
		})
	}
}

func (c *ircConnector) Post(_ context.Context, text string) error {
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	if client == nil {
		return fmt.Errorf("sessionrelay: irc client not connected")
	}
	for _, channel := range c.Channels() {
		client.Cmd.Message(channel, text)
	}
	return nil
}

func (c *ircConnector) PostTo(_ context.Context, channel, text string) error {
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	if client == nil {
		return fmt.Errorf("sessionrelay: irc client not connected")
	}
	channel = normalizeChannel(channel)
	if channel == "" {
		return fmt.Errorf("sessionrelay: post channel is required")
	}
	client.Cmd.Message(channel, text)
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

func (c *ircConnector) Touch(ctx context.Context) error {
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("sessionrelay: not connected")
	}

	if !client.IsConnected() {
		client.Close()
		select {
		case c.errCh <- fmt.Errorf("touch: client disconnected"):
		default:
		}
		return fmt.Errorf("sessionrelay: disconnected")
	}

	// Detect server restarts by checking the server's startup time.
	// If the server started after our IRC connection was established,
	// the IRC connection is stale and must be recycled.
	if c.apiURL != "" && c.token != "" {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, c.apiURL+"/v1/status", nil)
		if err != nil {
			return nil
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil // API unreachable, transient
		}
		defer resp.Body.Close()

		var status struct {
			Started string `json:"started"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&status); err == nil && status.Started != "" {
			serverStart, err := time.Parse(time.RFC3339Nano, status.Started)
			if err == nil {
				c.mu.RLock()
				connectedAt := c.connectedAt
				c.mu.RUnlock()
				if !connectedAt.IsZero() && serverStart.After(connectedAt) {
					// Server restarted after we connected — our IRC session is dead.
					client.Close()
					select {
					case c.errCh <- fmt.Errorf("touch: server restarted (started %s, connected %s)", serverStart.Format(time.RFC3339), connectedAt.Format(time.RFC3339)):
					default:
					}
					return fmt.Errorf("sessionrelay: server restarted")
				}
			}
		}

		// Also touch presence so the server tracks us.
		presenceReq, _ := http.NewRequestWithContext(probeCtx, http.MethodPost,
			c.apiURL+"/v1/channels/"+channelSlug(c.primary)+"/presence",
			bytes.NewReader([]byte(`{"nick":"`+c.nick+`"}`)))
		if presenceReq != nil {
			presenceReq.Header.Set("Authorization", "Bearer "+c.token)
			presenceReq.Header.Set("Content-Type", "application/json")
			pr, err := http.DefaultClient.Do(presenceReq)
			if pr != nil {
				pr.Body.Close()
			}
			_ = err
		}
	}

	return nil
}

func (c *ircConnector) JoinChannel(ctx context.Context, channel string) error {
	channel = normalizeChannel(channel)
	if channel == "" {
		return fmt.Errorf("sessionrelay: join channel is required")
	}
	c.mu.Lock()
	if slices.Contains(c.channels, channel) {
		c.mu.Unlock()
		return nil
	}
	c.channels = append(c.channels, channel)
	client := c.client
	c.mu.Unlock()
	if client != nil {
		client.Cmd.Join(channel)
	}
	go c.syncChannelsToRegistry(ctx)
	return nil
}

func (c *ircConnector) PartChannel(ctx context.Context, channel string) error {
	channel = normalizeChannel(channel)
	if channel == "" {
		return fmt.Errorf("sessionrelay: part channel is required")
	}
	if channel == c.primary {
		return fmt.Errorf("sessionrelay: cannot part control channel %s", channel)
	}
	c.mu.Lock()
	if !slices.Contains(c.channels, channel) {
		c.mu.Unlock()
		return nil
	}
	filtered := c.channels[:0]
	for _, existing := range c.channels {
		if existing == channel {
			continue
		}
		filtered = append(filtered, existing)
	}
	c.channels = filtered
	client := c.client
	c.mu.Unlock()
	if client != nil {
		client.Cmd.Part(channel)
	}
	go c.syncChannelsToRegistry(ctx)
	return nil
}

// syncChannelsToRegistry PATCHes the agent's channel list in the registry so
// the Agents tab stays in sync after live /join and /part commands.
func (c *ircConnector) syncChannelsToRegistry(ctx context.Context) {
	if c.apiURL == "" || c.token == "" || c.nick == "" {
		return
	}
	channels := c.Channels()
	body, err := json.Marshal(map[string]any{"channels": channels})
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.apiURL+"/v1/agents/"+c.nick, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

func (c *ircConnector) Channels() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.channels...)
}

func (c *ircConnector) ControlChannel() string {
	return c.primary
}

func (c *ircConnector) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.client != nil {
		c.client.Close()
		c.client = nil
	}
	c.mu.Unlock()
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
		"channels": c.Channels(),
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

func (c *ircConnector) hasChannel(channel string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Contains(c.channels, channel)
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
