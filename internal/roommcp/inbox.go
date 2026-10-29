package roommcp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type InboxRemote interface {
	Open(context.Context, SessionInput) (OpenResult, error)
	Read(context.Context, SessionInput) (ReadResult, error)
	Ack(context.Context, SessionInput, string) error
	Send(context.Context, SessionInput, string) error
}
type Event struct {
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta"`
}

// Inbox forwards only addressed messages from an explicit sender allowlist.
// Publishing is not processing: acknowledgments belong to the receiving agent.
type Inbox struct {
	Remote         InboxRemote
	Input          SessionInput
	AllowedSenders []string
	PollInterval   time.Duration
	Log            *slog.Logger
	sent           map[string]bool
	window         time.Time
	wakes          int
	opened         OpenResult
}

func (w *Inbox) matches(m Message) bool {
	if strings.EqualFold(m.Nick, w.opened.Nick) {
		return false
	}
	allowed := false
	for _, nick := range w.AllowedSenders {
		if strings.EqualFold(m.Nick, strings.TrimSpace(nick)) {
			allowed = true
			break
		}
	}
	if !allowed {
		return false
	}
	return addressedToAgent(m.Text, w.Input.Session, w.opened.Nick)
}

// Fleet handles are explicit addresses, scoped by the room being read. Runtime
// groups follow the existing runtime-prefixed logical session naming contract.
func addressedToAgent(text, session, nick string) bool {
	for _, target := range []string{session, nick, "agents"} {
		if target != "" && addressedTo(text, target) {
			return true
		}
	}
	runtime := strings.ToLower(strings.SplitN(session, "-", 2)[0])
	switch runtime {
	case "codex", "claude", "calliope":
		return addressedTo(text, runtime)
	}
	return false
}

// Only the leading recipient list routes a message. Incidental mentions in
// prose, quoted messages, and prefix matches must not wake an agent.
func addressedTo(text, name string) bool {
	text = strings.TrimSpace(text)
	for strings.HasPrefix(text, "@") {
		i := strings.IndexAny(text, " ,:\t\r\n")
		if i < 0 {
			i = len(text)
		}
		if strings.EqualFold(text[1:i], name) {
			return true
		}
		if i == len(text) || text[i] == ':' {
			return false
		}
		text = strings.TrimLeft(text[i:], " ,\t")
	}
	return false
}
func (w *Inbox) poll(ctx context.Context, emit func(context.Context, Event) error, now time.Time) error {
	batch, err := w.Remote.Read(ctx, w.Input)
	if err != nil {
		return err
	}
	if w.sent == nil {
		w.sent = map[string]bool{}
	}
	if w.window.IsZero() || now.Sub(w.window) >= time.Minute {
		w.window = now
		w.wakes = 0
	}
	present := map[string]bool{}
	for _, m := range batch.Messages {
		present[m.Cursor] = true
	}
	for cursor := range w.sent {
		if !present[cursor] {
			delete(w.sent, cursor)
		}
	}
	pending := false
	prefix := ""
	for _, m := range batch.Messages {
		if !w.matches(m) {
			if !pending {
				prefix = m.Cursor
			}
			continue
		}
		pending = true
		if w.sent[m.Cursor] || w.wakes >= 6 || len(w.sent) >= 6 {
			continue
		}
		event := Event{Content: m.Text, Meta: map[string]string{"session": w.Input.Session, "channel": w.opened.Channel, "sender": m.Nick, "cursor": m.Cursor}}
		if err := emit(ctx, event); err != nil {
			return err
		}
		w.sent[m.Cursor] = true
		w.wakes++
	}
	if prefix != "" {
		return w.Remote.Ack(ctx, w.Input, prefix)
	}
	if !pending && batch.NextCursor != "" && batch.NextCursor != "0" {
		return w.Remote.Ack(ctx, w.Input, batch.NextCursor)
	}
	return nil
}
func (w *Inbox) Run(ctx context.Context, emit func(context.Context, Event) error) error {
	if len(w.AllowedSenders) == 0 {
		return fmt.Errorf("at least one --allow-sender is required")
	}
	if _, err := validate(w.Input); err != nil {
		return err
	}
	if w.PollInterval == 0 {
		w.PollInterval = 2 * time.Second
	}
	if w.PollInterval < time.Second {
		return fmt.Errorf("poll interval must be at least one second")
	}
	if w.Log == nil {
		w.Log = slog.Default()
	}
	opened, err := w.Remote.Open(ctx, w.Input)
	if err != nil {
		return err
	}
	w.opened = opened
	w.Log.Info("native inbox connected", "session", w.Input.Session, "nick", opened.Nick, "channel", opened.Channel)
	timer := time.NewTimer(0)
	defer timer.Stop()
	lastPresence := time.Now()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		if err := w.poll(ctx, emit, time.Now()); err != nil {
			w.Log.Warn("native inbox polling failed; will retry", "err", err)
		}
		if time.Since(lastPresence) >= time.Minute {
			if _, err := w.Remote.Open(ctx, w.Input); err == nil {
				lastPresence = time.Now()
			}
		}
		timer.Reset(w.PollInterval)
	}
}
