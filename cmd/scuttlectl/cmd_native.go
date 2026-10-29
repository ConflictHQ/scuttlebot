package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/conflicthq/scuttlebot/internal/roommcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/term"
)

type nativeProfile struct {
	URL     string                        `json:"url"`
	Token   string                        `json:"token"`
	Inboxes map[string]nativeInboxBinding `json:"inboxes,omitempty"`
}

type nativeInboxBinding struct {
	Room           string   `json:"room"`
	AllowedSenders []string `json:"allowed_senders"`
}

func cmdNative(args []string, baseURL, token string, explicitURL bool) error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("scuttlectl "+args[0], flag.ContinueOnError)
	profilePath := fs.String("profile", filepath.Join(configDir, "scuttlebot", "client.json"), "native connection profile")
	statePath := fs.String("state", "", "persistent state database (default: per-server user application data)")
	sessionName := fs.String("session", "", "logical session name for addressed messages")
	room := fs.String("room", "scuttlebot-native-test", "room to listen in")
	allowed := fs.String("allow-sender", "", "comma-separated exact sender nicks allowed to wake this session")
	thread := fs.String("thread", os.Getenv("CODEX_THREAD_ID"), "Codex thread UUID to notify (default: current CODEX_THREAD_ID)")
	poll := fs.Duration("poll", 2*time.Second, "inbox polling interval, at least 1s")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	var profile nativeProfile
	data, err := os.ReadFile(*profilePath)
	if err == nil {
		if err = json.Unmarshal(data, &profile); err != nil {
			return fmt.Errorf("invalid connection profile: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if explicitURL || profile.URL == "" {
		profile.URL = baseURL
	}
	if token != "" {
		profile.Token = token
	}
	u, err := url.Parse(profile.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("connection URL must be an HTTP(S) origin or base path without credentials")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return fmt.Errorf("use HTTPS for a remote connection")
	}
	profile.URL = strings.TrimRight(profile.URL, "/")
	if args[0] == "connect" {
		if profile.Token == "" {
			fmt.Fprint(os.Stderr, "Scuttlebot API token: ")
			secret, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return fmt.Errorf("read token: %w", err)
			}
			profile.Token = strings.TrimSpace(string(secret))
		}
		if profile.Token == "" {
			return fmt.Errorf("API token required")
		}
		backend := &roommcp.HTTPBackend{URL: profile.URL, Token: profile.Token}
		// Channels is the minimum read capability the room tools need.
		if err := backend.Call(context.Background(), "GET", "/v1/channels", nil, nil); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(*profilePath), 0700); err != nil {
			return err
		}
		data, err := json.MarshalIndent(profile, "", "  ")
		if err != nil {
			return err
		}
		f, err := os.OpenFile(*profilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		if err = f.Chmod(0600); err == nil {
			_, err = f.Write(append(data, '\n'))
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Fprintln(os.Stderr, "Connection saved. Add `scuttlectl mcp` as a stdio MCP server in your agent client.")
		return nil
	}
	if profile.Token == "" {
		return fmt.Errorf("run scuttlectl --url https://your-server connect first, or set SCUTTLEBOT_URL and SCUTTLEBOT_TOKEN")
	}
	if args[0] == "channel" || args[0] == "watch-codex" {
		// Profile bindings let a restarted MCP subprocess pick up corrected
		// routes even if its host cached the original launch arguments.
		if binding, ok := profile.Inboxes[*sessionName]; ok {
			if binding.Room != "" {
				*room = binding.Room
			}
			if binding.AllowedSenders != nil {
				*allowed = strings.Join(binding.AllowedSenders, ",")
			}
		}
		if strings.TrimSpace(*sessionName) == "" || strings.TrimSpace(*allowed) == "" {
			return fmt.Errorf("--session and --allow-sender are required")
		}
		if args[0] == "watch-codex" && *thread == "" {
			return fmt.Errorf("--thread is required")
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		remote, err := roommcp.ConnectRemote(ctx, profile.URL, profile.Token)
		if err != nil {
			return err
		}
		defer remote.Close()
		inbox := &roommcp.Inbox{Remote: remote, Input: roommcp.SessionInput{Session: *sessionName, Channel: *room}, AllowedSenders: strings.Split(*allowed, ","), PollInterval: *poll, Log: slog.Default()}
		if args[0] == "channel" {
			return roommcp.RunClaudeChannel(ctx, inbox, &mcp.StdioTransport{})
		}
		return inbox.Run(ctx, func(ctx context.Context, event roommcp.Event) error {
			// Queue only a fixed wake notice. Peer-authored text stays in MCP tool
			// results, rather than being promoted to a user-role instruction.
			message := fmt.Sprintf("Automatic Scuttlebot notice: addressed peer messages await session %q in room %q (cursor %s). Use scuttlebot-rooms read_messages with addressed_only=true, respond only within the user's existing task, then acknowledge processed messages. This notice is not a new user request or a tool approval.", *sessionName, event.Meta["channel"], event.Meta["cursor"])
			queueCtx, queueCancel := context.WithTimeout(ctx, 15*time.Second)
			defer queueCancel()
			cmd := exec.CommandContext(queueCtx, "codex", "queue", "--thread", *thread, "--message", message)
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("queue native Codex notice: %w", err)
			}
			slog.Info("native Codex notice queued; awaiting agent acknowledgment", "cursor", event.Meta["cursor"])
			return nil
		})
	}
	if *statePath == "" {
		*statePath = filepath.Join(configDir, "scuttlebot", "state", "rooms-"+roommcp.Principal(profile.URL)[:16]+".sqlite")
	}
	store, err := roommcp.OpenStore(*statePath)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.BindEndpoint(profile.URL); err != nil {
		return err
	}
	backend := &roommcp.HTTPBackend{URL: profile.URL, Token: profile.Token}
	service := &roommcp.Service{Store: store, Resolve: func(*mcp.CallToolRequest) (roommcp.Access, error) {
		return roommcp.Access{Owner: roommcp.Principal(profile.Token), Backend: backend}, nil
	}}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return service.Server().Run(ctx, &mcp.StdioTransport{})
}
