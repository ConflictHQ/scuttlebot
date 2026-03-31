package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/conflicthq/scuttlebot/internal/api"
	"github.com/conflicthq/scuttlebot/internal/config"
	"github.com/conflicthq/scuttlebot/internal/ergo"
	"github.com/conflicthq/scuttlebot/internal/registry"
)

var version = "dev"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := &config.Config{}
	cfg.Defaults()
	cfg.ApplyEnv()

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

	// Start HTTP API server.
	apiToken := mustGenToken()
	log.Info("api token", "token", apiToken) // printed once on startup — user copies this
	apiSrv := api.New(reg, []string{apiToken}, log)
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

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)

	log.Info("goodbye")
}

func mustGenToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate token: %v\n", err)
		os.Exit(1)
	}
	return hex.EncodeToString(b)
}
