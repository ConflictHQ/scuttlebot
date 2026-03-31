// Package bots defines the Bot interface and shared types for all scuttlebot built-in bots.
package bots

import "context"

// Bot is the interface implemented by all scuttlebot built-in bots.
type Bot interface {
	// Name returns the bot's IRC nick.
	Name() string

	// Start connects the bot to IRC and begins processing messages.
	// Blocks until ctx is cancelled or a fatal error occurs.
	Start(ctx context.Context) error

	// Stop gracefully disconnects the bot.
	Stop()
}
