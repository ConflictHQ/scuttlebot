package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/conflicthq/scuttlebot/internal/roommcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/term"
)

type nativeProfile struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

func cmdNative(args []string, baseURL, token string, explicitURL bool) error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("scuttlectl "+args[0], flag.ContinueOnError)
	profilePath := fs.String("profile", filepath.Join(configDir, "scuttlebot", "client.json"), "native connection profile")
	statePath := fs.String("state", "", "persistent state database (default: per-server user application data)")
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
