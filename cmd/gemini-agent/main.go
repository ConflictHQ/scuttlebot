// gemini-agent is a thin wrapper around pkg/ircagent with Gemini defaults.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/conflicthq/scuttlebot/pkg/ircagent"
)

const systemPrompt = `You are an AI assistant connected to an IRC chat server called scuttlebot.
Be helpful, concise, and friendly. Keep responses short — IRC is a chat medium, not a document editor.
No markdown formatting (no **, ##, backtick blocks) — IRC renders plain text only.
You may use multiple lines but keep each thought brief.`

func main() {
	ircAddr := flag.String("irc", "127.0.0.1:6667", "IRC server address")
	nick := flag.String("nick", "gemini", "IRC nick")
	pass := flag.String("pass", "", "SASL password (required)")
	channels := flag.String("channels", "#general", "Comma-separated channels to join")
	apiKey := flag.String("api-key", os.Getenv("GEMINI_API_KEY"), "Gemini API key (direct mode)")
	model := flag.String("model", "gemini-1.5-flash", "Model override (direct mode)")
	apiURL := flag.String("api-url", "http://localhost:8080", "Scuttlebot API URL (gateway mode)")
	token := flag.String("token", os.Getenv("SCUTTLEBOT_TOKEN"), "Scuttlebot bearer token (gateway mode)")
	backend := flag.String("backend", "gemini", "Backend name in scuttlebot (gateway mode)")
	flag.Parse()

	if *pass == "" {
		fmt.Fprintln(os.Stderr, "error: --pass is required")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	err := ircagent.Run(ctx, ircagent.Config{
		IRCAddr:      *ircAddr,
		Nick:         *nick,
		Pass:         *pass,
		Channels:     ircagent.SplitCSV(*channels),
		SystemPrompt: systemPrompt,
		Logger:       slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
		ErrorJoiner:  " — ",
		Direct: &ircagent.DirectConfig{
			Backend: "gemini",
			APIKey:  *apiKey,
			Model:   *model,
		},
		Gateway: &ircagent.GatewayConfig{
			APIURL:  *apiURL,
			Token:   *token,
			Backend: *backend,
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
