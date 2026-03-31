package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/conflicthq/scuttlebot/internal/api"
	"github.com/conflicthq/scuttlebot/internal/bots/bridge"
	"github.com/conflicthq/scuttlebot/internal/config"
	"github.com/conflicthq/scuttlebot/internal/ergo"
	"github.com/conflicthq/scuttlebot/internal/mcp"
	"github.com/conflicthq/scuttlebot/internal/registry"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "scuttlebot.yaml", "path to config file (YAML)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := &config.Config{}
	cfg.Defaults()
	if err := cfg.LoadFile(*configPath); err != nil {
		log.Error("load config", "path", *configPath, "err", err)
		os.Exit(1)
	}
	cfg.ApplyEnv()

	// In managed mode, auto-fetch the ergo binary if not found.
	if !cfg.Ergo.External {
		binary, err := ergo.EnsureBinary(cfg.Ergo.BinaryPath, cfg.Ergo.DataDir)
		if err != nil {
			log.Error("ergo binary unavailable", "err", err)
			os.Exit(1)
		}
		abs, err := filepath.Abs(binary)
		if err != nil {
			log.Error("resolve ergo binary path", "err", err)
			os.Exit(1)
		}
		cfg.Ergo.BinaryPath = abs
	}

	// Generate an API token for the Ergo management API if not set.
	if cfg.Ergo.APIToken == "" {
		cfg.Ergo.APIToken = mustGenToken()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Info("scuttlebot starting", "version", version)

	// Start Ergo.
	manager := ergo.NewManager(cfg.Ergo, log)
	ergoErr := make(chan error, 1)
	go func() {
		if err := manager.Start(ctx); err != nil {
			ergoErr <- err
		}
	}()

	// Wait for Ergo to become healthy before starting the rest.
	healthCtx, healthCancel := context.WithTimeout(ctx, 30*time.Second)
	defer healthCancel()
	for {
		if _, err := manager.API().Status(); err == nil {
			break
		}
		select {
		case <-healthCtx.Done():
			log.Error("ergo did not become healthy in time")
			os.Exit(1)
		case err := <-ergoErr:
			log.Error("ergo failed to start", "err", err)
			os.Exit(1)
		case <-time.After(500 * time.Millisecond):
		}
	}
	log.Info("ergo healthy")

	// Build registry backed by Ergo's NickServ API.
	signingKey := []byte(mustGenToken())
	reg := registry.New(manager.API(), signingKey)

	// Shared API token — used by both REST and MCP servers.
	apiToken := mustGenToken()
	log.Info("api token", "token", apiToken) // printed once on startup — user copies this
	tokens := []string{apiToken}

	// Start bridge bot (powers the web chat UI).
	var bridgeBot *bridge.Bot
	if cfg.Bridge.Enabled {
		if cfg.Bridge.Password == "" {
			cfg.Bridge.Password = mustGenToken()
		}
		// Ensure the bridge's NickServ account exists with the current password.
		if err := manager.API().RegisterAccount(cfg.Bridge.Nick, cfg.Bridge.Password); err != nil {
			// Account exists from a previous run — update the password so it matches.
			if err2 := manager.API().ChangePassword(cfg.Bridge.Nick, cfg.Bridge.Password); err2 != nil {
				log.Error("bridge account setup failed", "err", err2)
				os.Exit(1)
			}
		}
		bridgeBot = bridge.New(
			cfg.Ergo.IRCAddr,
			cfg.Bridge.Nick,
			cfg.Bridge.Password,
			cfg.Bridge.Channels,
			cfg.Bridge.BufferSize,
			log,
		)
		go func() {
			if err := bridgeBot.Start(ctx); err != nil {
				log.Error("bridge bot error", "err", err)
			}
		}()
	}

	// Start HTTP REST API server.
	apiSrv := api.New(reg, tokens, bridgeBot, log)
	httpServer := &http.Server{
		Addr:    cfg.APIAddr,
		Handler: apiSrv.Handler(),
	}
	go func() {
		log.Info("api server listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("api server error", "err", err)
		}
	}()

	// Start MCP server.
	mcpSrv := mcp.New(reg, &ergoChannelLister{manager.API()}, tokens, log)
	mcpServer := &http.Server{
		Addr:    cfg.MCPAddr,
		Handler: mcpSrv.Handler(),
	}
	go func() {
		log.Info("mcp server listening", "addr", mcpServer.Addr)
		if err := mcpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("mcp server error", "err", err)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
	_ = mcpServer.Shutdown(shutdownCtx)

	log.Info("goodbye")
}

// ergoChannelLister adapts ergo.APIClient to mcp.ChannelLister.
type ergoChannelLister struct {
	api *ergo.APIClient
}

func (e *ergoChannelLister) ListChannels() ([]mcp.ChannelInfo, error) {
	resp, err := e.api.ListChannels()
	if err != nil {
		return nil, err
	}
	out := make([]mcp.ChannelInfo, len(resp.Channels))
	for i, ch := range resp.Channels {
		out[i] = mcp.ChannelInfo{
			Name:  ch.Name,
			Topic: ch.Topic,
			Count: ch.UserCount,
		}
	}
	return out, nil
}

func mustGenToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate token: %v\n", err)
		os.Exit(1)
	}
	return hex.EncodeToString(b)
}
