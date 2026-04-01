package client

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lrstanley/girc"
)

// DiscoveryOptions configures the caching behaviour for discovery calls.
type DiscoveryOptions struct {
	// CacheTTL is how long results are cached before re-querying Ergo.
	// Default: 30s. Set to 0 to disable caching.
	CacheTTL time.Duration
}

// ChannelSummary is a channel returned by ListChannels.
type ChannelSummary struct {
	Name        string
	MemberCount int
	Topic       string
}

// Member is an entry in the channel member list.
type Member struct {
	Nick    string
	IsOp    bool
	IsVoice bool
}

// TopicInfo is the result of GetTopic.
type TopicInfo struct {
	Channel string
	Topic   string
	SetBy   string
	SetAt   time.Time
}

// WhoIsInfo is the result of WhoIs.
type WhoIsInfo struct {
	Nick     string
	User     string
	Host     string
	RealName string
	Channels []string
	Account  string // NickServ account name, if identified
}

// discoveryCacheEntry wraps a result with an expiry.
type discoveryCacheEntry struct {
	value  any
	expiry time.Time
}

// discoveryCache is a simple TTL cache keyed by string.
type discoveryCache struct {
	mu      sync.Mutex
	entries map[string]discoveryCacheEntry
	ttl     time.Duration
}

func newDiscoveryCache(ttl time.Duration) *discoveryCache {
	return &discoveryCache{entries: make(map[string]discoveryCacheEntry), ttl: ttl}
}

func (c *discoveryCache) get(key string) (any, bool) {
	if c.ttl == 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiry) {
		return nil, false
	}
	return e.value, true
}

func (c *discoveryCache) set(key string, value any) {
	if c.ttl == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = discoveryCacheEntry{value: value, expiry: time.Now().Add(c.ttl)}
}

// Discovery wraps a connected Client with typed IRC discovery methods.
// Results are cached to avoid flooding Ergo.
//
// Typical usage:
//
//	d := client.NewDiscovery(c, client.DiscoveryOptions{CacheTTL: 30 * time.Second})
//	channels, err := d.ListChannels(ctx)
type Discovery struct {
	client *Client
	cache  *discoveryCache
}

// NewDiscovery creates a Discovery using the given (connected) Client.
func NewDiscovery(c *Client, opts DiscoveryOptions) *Discovery {
	ttl := opts.CacheTTL
	if ttl == 0 {
		ttl = 30 * time.Second
	}
	return &Discovery{client: c, cache: newDiscoveryCache(ttl)}
}

// ListChannels returns all public channels on the server.
func (d *Discovery) ListChannels(ctx context.Context) ([]ChannelSummary, error) {
	const cacheKey = "list_channels"
	if v, ok := d.cache.get(cacheKey); ok {
		return v.([]ChannelSummary), nil
	}

	irc := d.ircClient()
	if irc == nil {
		return nil, fmt.Errorf("discovery: not connected")
	}

	type item struct {
		name  string
		count int
		topic string
	}
	var (
		mu      sync.Mutex
		results []item
		done    = make(chan struct{})
	)

	listID := irc.Handlers.AddBg(girc.LIST, func(_ *girc.Client, e girc.Event) {
		// RPL_LIST: params are [me, channel, count, topic]
		if len(e.Params) < 3 {
			return
		}
		var count int
		_, _ = fmt.Sscanf(e.Params[2], "%d", &count)
		mu.Lock()
		results = append(results, item{e.Params[1], count, e.Last()})
		mu.Unlock()
	})

	endID := irc.Handlers.AddBg(girc.RPL_LISTEND, func(_ *girc.Client, _ girc.Event) {
		select {
		case done <- struct{}{}:
		default:
		}
	})

	defer func() {
		irc.Handlers.Remove(listID)
		irc.Handlers.Remove(endID)
	}()

	irc.Cmd.List()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
	}

	mu.Lock()
	defer mu.Unlock()
	out := make([]ChannelSummary, len(results))
	for i, r := range results {
		out[i] = ChannelSummary{Name: r.name, MemberCount: r.count, Topic: r.topic}
	}
	d.cache.set(cacheKey, out)
	return out, nil
}

// ChannelMembers returns the current member list for a channel.
func (d *Discovery) ChannelMembers(ctx context.Context, channel string) ([]Member, error) {
	cacheKey := "members:" + channel
	if v, ok := d.cache.get(cacheKey); ok {
		return v.([]Member), nil
	}

	irc := d.ircClient()
	if irc == nil {
		return nil, fmt.Errorf("discovery: not connected")
	}

	var (
		mu      sync.Mutex
		members []Member
		done    = make(chan struct{})
	)

	namesID := irc.Handlers.AddBg(girc.RPL_NAMREPLY, func(_ *girc.Client, e girc.Event) {
		// params: [me, mode, channel, names...]
		if len(e.Params) < 3 {
			return
		}
		// last param is space-separated nicks with optional @/+ prefix
		for _, n := range strings.Fields(e.Last()) {
			m := Member{Nick: n}
			if strings.HasPrefix(n, "@") {
				m.Nick = n[1:]
				m.IsOp = true
			} else if strings.HasPrefix(n, "+") {
				m.Nick = n[1:]
				m.IsVoice = true
			}
			mu.Lock()
			members = append(members, m)
			mu.Unlock()
		}
	})

	endID := irc.Handlers.AddBg(girc.RPL_ENDOFNAMES, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) >= 2 && strings.EqualFold(e.Params[1], channel) {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})

	defer func() {
		irc.Handlers.Remove(namesID)
		irc.Handlers.Remove(endID)
	}()

	_ = irc.Cmd.SendRaw("NAMES " + channel)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
	}

	mu.Lock()
	defer mu.Unlock()
	d.cache.set(cacheKey, members)
	return members, nil
}

// GetTopic returns the topic for a channel.
func (d *Discovery) GetTopic(ctx context.Context, channel string) (TopicInfo, error) {
	cacheKey := "topic:" + channel
	if v, ok := d.cache.get(cacheKey); ok {
		return v.(TopicInfo), nil
	}

	irc := d.ircClient()
	if irc == nil {
		return TopicInfo{}, fmt.Errorf("discovery: not connected")
	}

	result := make(chan TopicInfo, 1)

	topicID := irc.Handlers.AddBg(girc.RPL_TOPIC, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) >= 2 && strings.EqualFold(e.Params[1], channel) {
			select {
			case result <- TopicInfo{Channel: channel, Topic: e.Last()}:
			default:
			}
		}
	})

	noTopicID := irc.Handlers.AddBg(girc.RPL_NOTOPIC, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) >= 2 && strings.EqualFold(e.Params[1], channel) {
			select {
			case result <- TopicInfo{Channel: channel}:
			default:
			}
		}
	})

	defer func() {
		irc.Handlers.Remove(topicID)
		irc.Handlers.Remove(noTopicID)
	}()

	_ = irc.Cmd.SendRaw("TOPIC " + channel)

	select {
	case <-ctx.Done():
		return TopicInfo{}, ctx.Err()
	case info := <-result:
		d.cache.set(cacheKey, info)
		return info, nil
	}
}

// WhoIs returns identity information for a nick.
func (d *Discovery) WhoIs(ctx context.Context, nick string) (WhoIsInfo, error) {
	cacheKey := "whois:" + nick
	if v, ok := d.cache.get(cacheKey); ok {
		return v.(WhoIsInfo), nil
	}

	irc := d.ircClient()
	if irc == nil {
		return WhoIsInfo{}, fmt.Errorf("discovery: not connected")
	}

	var (
		mu   sync.Mutex
		info WhoIsInfo
		done = make(chan struct{})
	)
	info.Nick = nick

	// RPL_WHOISUSER (311): nick, user, host, *, realname
	userID := irc.Handlers.AddBg(girc.RPL_WHOISUSER, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 4 || !strings.EqualFold(e.Params[1], nick) {
			return
		}
		mu.Lock()
		info.User = e.Params[2]
		info.Host = e.Params[3]
		info.RealName = e.Last()
		mu.Unlock()
	})

	// RPL_WHOISCHANNELS (319): nick, channels
	chansID := irc.Handlers.AddBg(girc.RPL_WHOISCHANNELS, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 2 || !strings.EqualFold(e.Params[1], nick) {
			return
		}
		mu.Lock()
		for _, ch := range strings.Fields(e.Last()) {
			// Strip mode prefixes (@, +) from channel names.
			info.Channels = append(info.Channels, strings.TrimLeft(ch, "@+~&%"))
		}
		mu.Unlock()
	})

	// RPL_WHOISACCOUNT (330): nick, account, "is logged in as"
	acctID := irc.Handlers.AddBg(girc.RPL_WHOISACCOUNT, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 3 || !strings.EqualFold(e.Params[1], nick) {
			return
		}
		mu.Lock()
		info.Account = e.Params[2]
		mu.Unlock()
	})

	// RPL_ENDOFWHOIS (318)
	endID := irc.Handlers.AddBg(girc.RPL_ENDOFWHOIS, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) >= 2 && strings.EqualFold(e.Params[1], nick) {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})

	defer func() {
		irc.Handlers.Remove(userID)
		irc.Handlers.Remove(chansID)
		irc.Handlers.Remove(acctID)
		irc.Handlers.Remove(endID)
	}()

	irc.Cmd.Whois(nick)

	select {
	case <-ctx.Done():
		return WhoIsInfo{}, ctx.Err()
	case <-done:
	}

	mu.Lock()
	defer mu.Unlock()
	d.cache.set(cacheKey, info)
	return info, nil
}

// Invalidate removes a specific entry from the cache (e.g. on channel join/part events).
// key forms: "list_channels", "members:#fleet", "topic:#fleet", "whois:nick"
func (d *Discovery) Invalidate(key string) {
	d.cache.mu.Lock()
	delete(d.cache.entries, key)
	d.cache.mu.Unlock()
}

func (d *Discovery) ircClient() *girc.Client {
	d.client.mu.RLock()
	defer d.client.mu.RUnlock()
	return d.client.irc
}
