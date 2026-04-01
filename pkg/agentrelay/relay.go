// Package agentrelay lets any agent post status to a scuttlebot IRC channel
// and receive human instructions mid-work.
//
// Typical usage:
//
//	relay, err := agentrelay.New(agentrelay.Config{
//	    ServerURL: "http://localhost:8080",
//	    Token:     os.Getenv("SCUTTLEBOT_TOKEN"),
//	    Nick:      "my-agent",
//	    Channel:   "#fleet",
//	})
//	relay.Post("starting task: rewrite auth module")
//
//	// between steps, check for human instructions
//	if msg, ok := relay.Poll(); ok {
//	    // msg.Text is what the human said — incorporate or surface to agent
//	}
//
//	// or block waiting for approval
//	relay.Post("about to drop table users — approve?")
//	if err := relay.WaitFor("yes", 2*time.Minute); err != nil {
//	    // timed out or got "no" — abort
//	}
package agentrelay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config configures a Relay.
type Config struct {
	// ServerURL is the scuttlebot HTTP API base URL.
	ServerURL string
	// Token is the scuttlebot bearer token.
	Token string
	// Nick is this agent's IRC nick — used to filter out its own messages.
	Nick string
	// Channel is the IRC channel to post to and listen on.
	Channel string
}

// Message is an inbound message from the channel.
type Message struct {
	At      time.Time `json:"at"`
	Nick    string    `json:"nick"`
	Text    string    `json:"text"`
	Channel string    `json:"channel"`
}

// Relay posts status messages to an IRC channel and surfaces inbound
// human messages to the running agent. It is safe for concurrent use.
type Relay struct {
	cfg  Config
	http *http.Client

	mu     sync.Mutex
	inbox  []Message // buffered inbound messages not yet consumed
	cancel context.CancelFunc
}

// New creates a Relay and starts listening for inbound messages via SSE.
// Call Close when done.
func New(cfg Config) (*Relay, error) {
	if cfg.ServerURL == "" || cfg.Token == "" || cfg.Nick == "" || cfg.Channel == "" {
		return nil, fmt.Errorf("agentrelay: ServerURL, Token, Nick, and Channel are required")
	}
	r := &Relay{
		cfg:  cfg,
		http: &http.Client{Timeout: 0}, // no timeout for SSE
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go r.streamLoop(ctx)
	return r, nil
}

// Post sends a status message to the channel. Non-blocking.
func (r *Relay) Post(text string) error {
	body, _ := json.Marshal(map[string]string{
		"text": text,
		"nick": r.cfg.Nick,
	})
	slug := strings.TrimPrefix(r.cfg.Channel, "#")
	req, err := http.NewRequest("POST", r.cfg.ServerURL+"/v1/channels/"+slug+"/messages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.cfg.Token)
	resp, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("agentrelay post: %w", err)
	}
	resp.Body.Close()
	return nil
}

// Poll returns the oldest unread inbound message, if any.
// Returns false if there are no pending messages.
func (r *Relay) Poll() (Message, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.inbox) == 0 {
		return Message{}, false
	}
	msg := r.inbox[0]
	r.inbox = r.inbox[1:]
	return msg, true
}

// Drain returns all buffered inbound messages and clears the inbox.
func (r *Relay) Drain() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	msgs := r.inbox
	r.inbox = nil
	return msgs
}

// WaitFor blocks until a message containing keyword arrives (case-insensitive),
// or until timeout. Returns an error if the timeout elapses or a message
// containing "no" or "stop" arrives first.
func (r *Relay) WaitFor(keyword string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		msg, ok := r.Poll()
		if ok {
			lower := strings.ToLower(msg.Text)
			if strings.Contains(lower, strings.ToLower(keyword)) {
				return nil
			}
			if strings.Contains(lower, "no") || strings.Contains(lower, "stop") || strings.Contains(lower, "abort") {
				return fmt.Errorf("agentrelay: operator said %q", msg.Text)
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("agentrelay: timed out waiting for %q", keyword)
}

// Close stops the background SSE listener.
func (r *Relay) Close() {
	if r.cancel != nil {
		r.cancel()
	}
}

// streamLoop maintains an SSE connection and feeds inbound messages into inbox.
func (r *Relay) streamLoop(ctx context.Context) {
	slug := strings.TrimPrefix(r.cfg.Channel, "#")
	url := r.cfg.ServerURL + "/v1/channels/" + slug + "/stream?token=" + r.cfg.Token

	for {
		if ctx.Err() != nil {
			return
		}
		if err := r.stream(ctx, url); err != nil && ctx.Err() == nil {
			// Back off briefly before reconnecting.
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}
}

func (r *Relay) stream(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]
		var msg Message
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			continue
		}
		// Skip own messages and heartbeats.
		if msg.Nick == r.cfg.Nick || msg.Text == "" {
			continue
		}
		r.mu.Lock()
		r.inbox = append(r.inbox, msg)
		r.mu.Unlock()
	}
	return scanner.Err()
}

// Postf is a convenience wrapper for fmt.Sprintf + Post.
func (r *Relay) Postf(format string, args ...any) error {
	return r.Post(fmt.Sprintf(format, args...))
}

// MustPost posts and panics on error. Useful in quick scripts.
func (r *Relay) MustPost(text string) {
	if err := r.Post(text); err != nil {
		panic(err)
	}
}

// FetchHistory returns recent messages from the channel (for catching up).
func (r *Relay) FetchHistory(limit int) ([]Message, error) {
	slug := strings.TrimPrefix(r.cfg.Channel, "#")
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/v1/channels/%s/messages", r.cfg.ServerURL, slug), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.cfg.Token)
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agentrelay history: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var result struct {
		Messages []Message `json:"messages"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("agentrelay history parse: %w", err)
	}
	msgs := result.Messages
	if limit > 0 && len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
	}
	// Filter out own messages.
	out := msgs[:0]
	for _, m := range msgs {
		if m.Nick != r.cfg.Nick {
			out = append(out, m)
		}
	}
	return out, nil
}
