// Package client provides a Go SDK for connecting agents to scuttlebot.
//
// Agents use this package to join IRC channels, send structured messages, and
// register handlers for incoming message types. IRC is abstracted entirely —
// callers think in terms of channels and message types, not IRC primitives.
//
// Quick start:
//
//	c, err := client.New(client.Options{
//	    ServerAddr: "127.0.0.1:6667",
//	    Nick:       "my-agent",
//	    Password:   creds.Password,
//	    Channels:   []string{"#fleet"},
//	})
//	c.Handle("task.create", func(ctx context.Context, env *protocol.Envelope) error {
//	    // handle the task
//	    return nil
//	})
//	err = c.Run(ctx)
package client

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lrstanley/girc"

	"github.com/conflicthq/scuttlebot/pkg/protocol"
)

const (
	reconnectBase = 2 * time.Second
	reconnectMax  = 60 * time.Second
)

// HandlerFunc is called when a message of a registered type arrives.
// Returning a non-nil error logs it but does not disconnect the client.
type HandlerFunc func(ctx context.Context, env *protocol.Envelope) error

// Options configures a Client.
type Options struct {
	// ServerAddr is the IRC server address (host:port).
	ServerAddr string

	// Nick is the IRC nick and NickServ account name.
	Nick string

	// Password is the NickServ / SASL password received at registration.
	Password string

	// Channels is the list of IRC channels to join on connect.
	Channels []string

	// Log is an optional structured logger. Defaults to discarding all output.
	Log *slog.Logger
}

// Client connects an agent to scuttlebot over IRC.
type Client struct {
	opts     Options
	log      *slog.Logger
	mu       sync.RWMutex
	handlers map[string][]HandlerFunc
	irc      *girc.Client
}

// New creates a Client. Call Run to connect.
func New(opts Options) (*Client, error) {
	if opts.ServerAddr == "" {
		return nil, fmt.Errorf("client: ServerAddr is required")
	}
	if opts.Nick == "" {
		return nil, fmt.Errorf("client: Nick is required")
	}
	if opts.Password == "" {
		return nil, fmt.Errorf("client: Password is required")
	}
	log := opts.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(noopWriter{}, nil))
	}
	return &Client{
		opts:     opts,
		log:      log,
		handlers: make(map[string][]HandlerFunc),
	}, nil
}

// Handle registers a handler for messages of the given type (e.g. "task.create").
// Multiple handlers can be registered for the same type; they run concurrently.
// Use "*" to receive every message type.
func (c *Client) Handle(msgType string, fn HandlerFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[msgType] = append(c.handlers[msgType], fn)
}

// Send encodes payload as a protocol.Envelope of the given type and sends it
// to channel as a PRIVMSG.
func (c *Client) Send(ctx context.Context, channel, msgType string, payload any) error {
	env, err := protocol.New(msgType, c.opts.Nick, payload)
	if err != nil {
		return fmt.Errorf("client: build envelope: %w", err)
	}
	data, err := protocol.Marshal(env)
	if err != nil {
		return fmt.Errorf("client: marshal envelope: %w", err)
	}

	c.mu.RLock()
	irc := c.irc
	c.mu.RUnlock()

	if irc == nil {
		return fmt.Errorf("client: not connected")
	}
	irc.Cmd.Message(channel, string(data))
	return nil
}

// Run connects to IRC and blocks until ctx is cancelled. It reconnects with
// exponential backoff if the connection drops.
func (c *Client) Run(ctx context.Context) error {
	wait := reconnectBase
	for {
		if ctx.Err() != nil {
			return nil
		}

		err := c.connect(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			c.log.Warn("irc connection error, reconnecting", "err", err, "wait", wait)
		} else {
			c.log.Info("irc disconnected, reconnecting", "wait", wait)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait = minDuration(wait*2, reconnectMax)
	}
}

// connect makes one connection attempt. Blocks until disconnected or ctx done.
func (c *Client) connect(ctx context.Context) error {
	host, port, err := splitHostPort(c.opts.ServerAddr)
	if err != nil {
		return fmt.Errorf("parse server addr: %w", err)
	}

	irc := girc.New(girc.Config{
		Server: host,
		Port:   port,
		Nick:   c.opts.Nick,
		User:   c.opts.Nick,
		Name:   c.opts.Nick,
		SASL:   &girc.SASLPlain{User: c.opts.Nick, Pass: c.opts.Password},
		SSL:    false,
	})

	irc.Handlers.AddBg(girc.CONNECTED, func(cl *girc.Client, e girc.Event) {
		for _, ch := range c.opts.Channels {
			cl.Cmd.Join(ch)
		}
		c.log.Info("connected to irc", "server", c.opts.ServerAddr, "channels", c.opts.Channels)
		// Reset backoff in caller on successful connect.
	})

	irc.Handlers.AddBg(girc.PRIVMSG, func(cl *girc.Client, e girc.Event) {
		if len(e.Params) < 1 {
			return
		}
		channel := e.Params[0]
		if !strings.HasPrefix(channel, "#") {
			return // ignore DMs
		}
		text := e.Last()
		env, err := protocol.Unmarshal([]byte(text))
		if err != nil {
			return // non-JSON PRIVMSG (human chat) — silently ignored
		}
		c.dispatch(ctx, env)
	})

	// NOTICE is ignored — system/human commentary, not agent traffic.

	c.mu.Lock()
	c.irc = irc
	c.mu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		if err := irc.Connect(); err != nil {
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	select {
	case <-ctx.Done():
		irc.Close()
		c.mu.Lock()
		c.irc = nil
		c.mu.Unlock()
		return nil
	case err := <-errCh:
		c.mu.Lock()
		c.irc = nil
		c.mu.Unlock()
		return err
	}
}

// dispatch delivers an envelope to all matching handlers, each in its own goroutine.
func (c *Client) dispatch(ctx context.Context, env *protocol.Envelope) {
	c.mu.RLock()
	typed := append([]HandlerFunc(nil), c.handlers[env.Type]...)
	wild := append([]HandlerFunc(nil), c.handlers["*"]...)
	c.mu.RUnlock()

	fns := append(typed, wild...)
	for _, fn := range fns {
		fn := fn
		go func() {
			if err := fn(ctx, env); err != nil {
				c.log.Error("handler error", "type", env.Type, "id", env.ID, "err", err)
			}
		}()
	}
}

func splitHostPort(addr string) (string, int, error) {
	var host string
	var port int
	if _, err := fmt.Sscanf(addr, "%[^:]:%d", &host, &port); err != nil {
		return "", 0, fmt.Errorf("invalid address %q: %w", addr, err)
	}
	return host, port, nil
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// noopWriter satisfies io.Writer for the discard logger.
type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }
