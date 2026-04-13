// Package chathistory provides a synchronous wrapper around the IRCv3
// CHATHISTORY extension for use with girc clients.
//
// Usage:
//
//	fetcher := chathistory.New(client)
//	msgs, err := fetcher.Latest(ctx, "#channel", 50)
package chathistory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lrstanley/girc"
)

// Message is a single message returned by a CHATHISTORY query.
type Message struct {
	At      time.Time
	Nick    string
	Account string
	Text    string
	MsgID   string
}

// Fetcher sends CHATHISTORY commands and collects the batched responses.
type Fetcher struct {
	client *girc.Client

	mu       sync.Mutex
	batches  map[string]*batch         // batchRef → accumulator
	waiters  map[string]chan []Message // channel → result (one waiter per channel)
	handlers bool
}

type batch struct {
	channel string
	msgs    []Message
}

// New creates a Fetcher and registers the necessary BATCH handlers on the
// client. The client's Config.SupportedCaps should include
// "draft/chathistory" (or "chathistory") so the capability is negotiated.
func New(client *girc.Client) *Fetcher {
	f := &Fetcher{
		client:  client,
		batches: make(map[string]*batch),
		waiters: make(map[string]chan []Message),
	}
	f.registerHandlers()
	return f
}

func (f *Fetcher) registerHandlers() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.handlers {
		return
	}
	f.handlers = true

	// BATCH open/close.
	f.client.Handlers.AddBg("BATCH", func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 {
			return
		}
		raw := e.Params[0]
		if strings.HasPrefix(raw, "+") {
			ref := raw[1:]
			if len(e.Params) >= 2 && e.Params[1] == "chathistory" {
				ch := ""
				if len(e.Params) >= 3 {
					ch = e.Params[2]
				}
				f.mu.Lock()
				f.batches[ref] = &batch{channel: ch}
				f.mu.Unlock()
			}
		} else if strings.HasPrefix(raw, "-") {
			ref := raw[1:]
			f.mu.Lock()
			b, ok := f.batches[ref]
			if ok {
				delete(f.batches, ref)
				if w, wok := f.waiters[b.channel]; wok {
					delete(f.waiters, b.channel)
					f.mu.Unlock()
					w <- b.msgs
					return
				}
			}
			f.mu.Unlock()
		}
	})

	// Collect PRIVMSGs tagged with a tracked batch ref.
	f.client.Handlers.AddBg(girc.PRIVMSG, func(_ *girc.Client, e girc.Event) {
		batchRef, ok := e.Tags.Get("batch")
		if !ok || batchRef == "" {
			return
		}

		f.mu.Lock()
		b, tracked := f.batches[batchRef]
		if !tracked {
			f.mu.Unlock()
			return
		}

		nick := ""
		if e.Source != nil {
			nick = e.Source.Name
		}
		acct, _ := e.Tags.Get("account")
		msgID, _ := e.Tags.Get("msgid")

		b.msgs = append(b.msgs, Message{
			At:      e.Timestamp,
			Nick:    nick,
			Account: acct,
			Text:    e.Last(),
			MsgID:   msgID,
		})
		f.mu.Unlock()
	})
}

// Latest fetches the N most recent messages from a channel using
// CHATHISTORY LATEST. Blocks until the server responds or ctx expires.
func (f *Fetcher) Latest(ctx context.Context, channel string, count int) ([]Message, error) {
	result := make(chan []Message, 1)

	f.mu.Lock()
	f.waiters[channel] = result
	f.mu.Unlock()

	if err := f.client.Cmd.SendRawf("CHATHISTORY LATEST %s * %d", channel, count); err != nil {
		f.mu.Lock()
		delete(f.waiters, channel)
		f.mu.Unlock()
		return nil, fmt.Errorf("chathistory: send: %w", err)
	}

	select {
	case msgs := <-result:
		return msgs, nil
	case <-ctx.Done():
		f.mu.Lock()
		delete(f.waiters, channel)
		f.mu.Unlock()
		return nil, ctx.Err()
	}
}

// Before fetches up to count messages before the given timestamp.
func (f *Fetcher) Before(ctx context.Context, channel string, before time.Time, count int) ([]Message, error) {
	result := make(chan []Message, 1)

	f.mu.Lock()
	f.waiters[channel] = result
	f.mu.Unlock()

	ts := before.UTC().Format("2006-01-02T15:04:05.000Z")
	if err := f.client.Cmd.SendRawf("CHATHISTORY BEFORE %s timestamp=%s %d", channel, ts, count); err != nil {
		f.mu.Lock()
		delete(f.waiters, channel)
		f.mu.Unlock()
		return nil, fmt.Errorf("chathistory: send: %w", err)
	}

	select {
	case msgs := <-result:
		return msgs, nil
	case <-ctx.Done():
		f.mu.Lock()
		delete(f.waiters, channel)
		f.mu.Unlock()
		return nil, ctx.Err()
	}
}
