package sessionrelay

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	defaultRequestTimeout = 3 * time.Second
	defaultBufferSize     = 512
)

type Transport string

const (
	TransportHTTP Transport = "http"
	TransportIRC  Transport = "irc"
)

type Config struct {
	Transport  Transport
	URL        string
	Token      string
	Channel    string
	Nick       string
	HTTPClient *http.Client
	IRC        IRCConfig
}

type IRCConfig struct {
	Addr          string
	Pass          string
	AgentType     string
	DeleteOnClose bool
}

type Message struct {
	At   time.Time
	Nick string
	Text string
}

type Connector interface {
	Connect(ctx context.Context) error
	Post(ctx context.Context, text string) error
	MessagesSince(ctx context.Context, since time.Time) ([]Message, error)
	Touch(ctx context.Context) error
	Close(ctx context.Context) error
}

func New(cfg Config) (Connector, error) {
	cfg = withDefaults(cfg)
	if err := validateBaseConfig(cfg); err != nil {
		return nil, err
	}

	switch cfg.Transport {
	case TransportHTTP:
		return newHTTPConnector(cfg), nil
	case TransportIRC:
		return newIRCConnector(cfg)
	default:
		return nil, fmt.Errorf("sessionrelay: unsupported transport %q", cfg.Transport)
	}
}

func withDefaults(cfg Config) Config {
	if cfg.Transport == "" {
		cfg.Transport = TransportHTTP
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: defaultRequestTimeout}
	}
	if cfg.IRC.AgentType == "" {
		cfg.IRC.AgentType = "worker"
	}
	cfg.Channel = normalizeChannel(cfg.Channel)
	cfg.Transport = Transport(strings.ToLower(string(cfg.Transport)))
	return cfg
}

func validateBaseConfig(cfg Config) error {
	if cfg.Channel == "" {
		return fmt.Errorf("sessionrelay: channel is required")
	}
	if cfg.Nick == "" {
		return fmt.Errorf("sessionrelay: nick is required")
	}
	return nil
}

func normalizeChannel(channel string) string {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return ""
	}
	if strings.HasPrefix(channel, "#") {
		return channel
	}
	return "#" + channel
}

func channelSlug(channel string) string {
	return strings.TrimPrefix(normalizeChannel(channel), "#")
}
