package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/conflicthq/scuttlebot/internal/api"
	"github.com/conflicthq/scuttlebot/internal/auth"
	"github.com/conflicthq/scuttlebot/internal/bots/bridge"
	botmanager "github.com/conflicthq/scuttlebot/internal/bots/manager"
	"github.com/conflicthq/scuttlebot/internal/config"
	"github.com/conflicthq/scuttlebot/internal/ergo"
	"github.com/conflicthq/scuttlebot/internal/mcp"
	"github.com/conflicthq/scuttlebot/internal/registry"
	"github.com/conflicthq/scuttlebot/internal/store"
	"github.com/conflicthq/scuttlebot/internal/topology"
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

	// Load or generate a stable Ergo management API token.
	// We persist it to data/ergo-api-token so it survives restarts — without
	// this the token changes every launch and the NickServ password-rotation
	// API call fails with 401 because ergo already loaded the old token.
	ergoTokenPath := filepath.Join(cfg.Ergo.DataDir, "ergo-api-token")
	if cfg.Ergo.APIToken == "" {
		if raw, err := os.ReadFile(ergoTokenPath); err == nil && len(raw) > 0 {
			cfg.Ergo.APIToken = strings.TrimSpace(string(raw))
		} else {
			cfg.Ergo.APIToken = mustGenToken()
			_ = os.WriteFile(ergoTokenPath, []byte(cfg.Ergo.APIToken), 0600)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Info("scuttlebot starting", "version", version)

	// Start Ergo.
	ergoMgr := ergo.NewManager(cfg.Ergo, log)
	ergoErr := make(chan error, 1)
	go func() {
		if err := ergoMgr.Start(ctx); err != nil {
			ergoErr <- err
		}
	}()

	// Wait for Ergo to become healthy before starting the rest.
	healthCtx, healthCancel := context.WithTimeout(ctx, 30*time.Second)
	defer healthCancel()
	for {
		if _, err := ergoMgr.API().Status(); err == nil {
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

	// Open datastore if configured (SQLite or PostgreSQL).
	// When not configured, all stores fall back to JSON files in data/.
	var dataStore *store.Store
	if cfg.Datastore.Driver != "" && cfg.Datastore.DSN != "" {
		ds, err := store.Open(cfg.Datastore.Driver, cfg.Datastore.DSN)
		if err != nil {
			log.Error("datastore open", "driver", cfg.Datastore.Driver, "err", err)
			os.Exit(1)
		}
		defer ds.Close()
		dataStore = ds
		log.Info("datastore open", "driver", cfg.Datastore.Driver)
	}

	// Build registry backed by Ergo's NickServ API.
	// Signing key persists so issued payloads stay valid across restarts.
	signingKeyHex, err := loadOrCreateToken(filepath.Join(cfg.Ergo.DataDir, "signing_key"))
	if err != nil {
		log.Error("signing key", "err", err)
		os.Exit(1)
	}
	reg := registry.New(ergoMgr.API(), []byte(signingKeyHex))
	if dataStore != nil {
		if err := reg.SetStore(dataStore); err != nil {
			log.Error("registry load from store", "err", err)
			os.Exit(1)
		}
	} else if err := reg.SetDataPath(filepath.Join(cfg.Ergo.DataDir, "registry.json")); err != nil {
		log.Error("registry load", "err", err)
		os.Exit(1)
	}

	// API key store — per-consumer tokens with scoped permissions.
	apiKeyStore, err := auth.NewAPIKeyStore(filepath.Join(cfg.Ergo.DataDir, "api_keys.json"))
	if err != nil {
		log.Error("api key store", "err", err)
		os.Exit(1)
	}
	// Migrate legacy api_token into key store on first run.
	if apiKeyStore.IsEmpty() {
		apiToken, err := loadOrCreateToken(filepath.Join(cfg.Ergo.DataDir, "api_token"))
		if err != nil {
			log.Error("api token", "err", err)
			os.Exit(1)
		}
		if _, err := apiKeyStore.Insert("server", apiToken, []auth.Scope{auth.ScopeAdmin}); err != nil {
			log.Error("migrate api token to key store", "err", err)
			os.Exit(1)
		}
		log.Info("migrated api_token to api_keys.json", "token", apiToken)
	} else {
		log.Info("api key store loaded", "keys", len(apiKeyStore.List()))
	}

	// Start bridge bot (powers the web chat UI).
	var bridgeBot *bridge.Bot
	if cfg.Bridge.Enabled {
		if cfg.Bridge.Password == "" {
			cfg.Bridge.Password = mustGenToken()
		}
		// Ensure the bridge's NickServ account exists with the current password.
		if err := ergoMgr.API().RegisterAccount(cfg.Bridge.Nick, cfg.Bridge.Password); err != nil {
			// Account exists from a previous run — update the password so it matches.
			if err2 := ergoMgr.API().ChangePassword(cfg.Bridge.Nick, cfg.Bridge.Password); err2 != nil {
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
			time.Duration(cfg.Bridge.WebUserTTLMinutes)*time.Minute,
			log,
		)
		go func() {
			if err := bridgeBot.Start(ctx); err != nil {
				log.Error("bridge bot error", "err", err)
			}
		}()
	}

	// Topology manager — provisions static channels and enforces autojoin policy.
	var topoMgr *topology.Manager
	if len(cfg.Topology.Channels) > 0 || len(cfg.Topology.Types) > 0 {
		topoPolicy := topology.NewPolicy(cfg.Topology)
		topoPass := mustGenToken()
		if err := ergoMgr.API().RegisterAccount(cfg.Topology.Nick, topoPass); err != nil {
			if err2 := ergoMgr.API().ChangePassword(cfg.Topology.Nick, topoPass); err2 != nil {
				log.Error("topology account setup failed", "err", err2)
				os.Exit(1)
			}
		}
		topoMgr = topology.NewManager(cfg.Ergo.IRCAddr, cfg.Topology.Nick, topoPass, cfg.Ergo.APIToken, topoPolicy, log)
		topoCtx, topoCancel := context.WithTimeout(ctx, 30*time.Second)
		if err := topoMgr.Connect(topoCtx); err != nil {
			topoCancel()
			log.Error("topology manager connect failed", "err", err)
			os.Exit(1)
		}
		topoCancel()
		staticChannels := make([]topology.ChannelConfig, 0, len(cfg.Topology.Channels))
		for _, sc := range cfg.Topology.Channels {
			staticChannels = append(staticChannels, topology.ChannelConfig{
				Name:     sc.Name,
				Topic:    sc.Topic,
				Ops:      sc.Ops,
				Voice:    sc.Voice,
				Autojoin: sc.Autojoin,
				Modes:    sc.Modes,
			})
		}
		go func() {
			if err := topoMgr.Provision(staticChannels); err != nil {
				log.Error("topology provision failed", "err", err)
			}
		}()
		topoMgr.StartReaper(ctx)
		go func() {
			<-ctx.Done()
			topoMgr.Close()
		}()
	}

	// Policy store — persists behavior/agent/logging settings.
	policyStore, err := api.NewPolicyStore(filepath.Join(cfg.Ergo.DataDir, "policies.json"), cfg.Bridge.WebUserTTLMinutes)
	if err != nil {
		log.Error("policy store", "err", err)
		os.Exit(1)
	}
	if dataStore != nil {
		if err := policyStore.SetStore(dataStore); err != nil {
			log.Error("policy store load from db", "err", err)
			os.Exit(1)
		}
	}
	if bridgeBot != nil {
		bridgeBot.SetWebUserTTL(time.Duration(policyStore.Get().Bridge.WebUserTTLMinutes) * time.Minute)
		// Deliver on-join instructions when agents join channels.
		// Skip system bots to avoid flooding the bridge IRC connection on startup.
		bridgeBot.SetOnUserJoin(func(channel, nick string) {
			// Don't send on-join to system bots — they already know what to do.
			systemBots := map[string]bool{
				"bridge": true, "auditbot": true, "scribe": true, "herald": true,
				"oracle": true, "warden": true, "scroll": true, "systembot": true,
				"snitch": true, "sentinel": true, "steward": true, "shepherd": true,
				"topology": true,
			}
			if systemBots[nick] {
				return
			}
			p := policyStore.Get()
			// Per-channel override wins; otherwise fall back to the global default
			// orientation message so every joining agent gets context, even on
			// dynamically-created channels with no specific template.
			msg, ok := p.OnJoinMessages[channel]
			if !ok || msg == "" {
				msg = p.OnJoinDefault
			}
			if msg == "" {
				return
			}
			msg = strings.ReplaceAll(msg, "{nick}", nick)
			msg = strings.ReplaceAll(msg, "{channel}", channel)
			for _, line := range strings.Split(msg, "\n") {
				if line != "" {
					bridgeBot.Notice(nick, line)
				}
			}
		})
	}

	// Admin store — bcrypt-hashed admin accounts.
	adminStore, err := auth.NewAdminStore(filepath.Join(cfg.Ergo.DataDir, "admins.json"))
	if err != nil {
		log.Error("admin store", "err", err)
		os.Exit(1)
	}
	if dataStore != nil {
		if err := adminStore.SetStore(dataStore); err != nil {
			log.Error("admin store load from db", "err", err)
			os.Exit(1)
		}
	}
	if adminStore.IsEmpty() {
		password := mustGenToken()[:16]
		if err := adminStore.Add("admin", password); err != nil {
			log.Error("create default admin", "err", err)
			os.Exit(1)
		}
		log.Info("first run — default admin created", "username", "admin", "password", password, "action", "change this password immediately")
	}

	// Bot manager — starts/stops system bots based on policy.
	botMgr := botmanager.New(cfg.Ergo.IRCAddr, cfg.Ergo.DataDir, ergoMgr.API(), &ergoChannelListAdapter{ergoMgr.API()}, log)

	// Wire policy onChange to re-sync bots on every policy update.
	policyStore.OnChange(func(p api.Policies) {
		specs := make([]botmanager.BotSpec, len(p.Behaviors))
		for i, b := range p.Behaviors {
			specs[i] = botmanager.BotSpec{
				ID:               b.ID,
				Nick:             b.Nick,
				Enabled:          b.Enabled,
				JoinAllChannels:  b.JoinAllChannels,
				RequiredChannels: b.RequiredChannels,
				Config:           b.Config,
			}
		}
		if bridgeBot != nil {
			bridgeBot.SetWebUserTTL(time.Duration(p.Bridge.WebUserTTLMinutes) * time.Minute)
		}
		if p.AgentPolicy.OnlineTimeoutSecs > 0 {
			reg.SetOnlineTimeout(time.Duration(p.AgentPolicy.OnlineTimeoutSecs) * time.Second)
		}
		botMgr.Sync(ctx, specs)
	})

	// Initial bot sync — delayed to let the bridge create channels first.
	// Bots with join_all_channels need channels to exist before they query
	// the channel list. The bridge takes ~2s to connect and join.
	go func() {
		time.Sleep(5 * time.Second)
		p := policyStore.Get()
		specs := make([]botmanager.BotSpec, len(p.Behaviors))
		for i, b := range p.Behaviors {
			specs[i] = botmanager.BotSpec{
				ID:               b.ID,
				Nick:             b.Nick,
				Enabled:          b.Enabled,
				JoinAllChannels:  b.JoinAllChannels,
				RequiredChannels: b.RequiredChannels,
				Config:           b.Config,
			}
		}
		botMgr.Sync(ctx, specs)
	}()

	// Agent reaper — periodically removes stale agents based on policy.
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p := policyStore.Get()
				if p.AgentPolicy.ReapAfterDays > 0 {
					maxAge := time.Duration(p.AgentPolicy.ReapAfterDays) * 24 * time.Hour
					if n := reg.Reap(maxAge); n > 0 {
						log.Info("reaped stale agents", "count", n, "max_age_days", p.AgentPolicy.ReapAfterDays)
					}
				}
			}
		}
	}()

	// Config store — owns write-back to scuttlebot.yaml with history snapshots.
	cfgStore := api.NewConfigStore(*configPath, *cfg)
	cfgStore.OnChange(func(updated config.Config) {
		// Hot-reload topology on config change.
		if topoMgr != nil {
			staticChannels := make([]topology.ChannelConfig, 0, len(updated.Topology.Channels))
			for _, sc := range updated.Topology.Channels {
				staticChannels = append(staticChannels, topology.ChannelConfig{
					Name: sc.Name, Topic: sc.Topic,
					Ops: sc.Ops, Voice: sc.Voice, Autojoin: sc.Autojoin,
					Modes: sc.Modes,
				})
			}
			if err := topoMgr.Provision(staticChannels); err != nil {
				log.Error("topology hot-reload failed", "err", err)
			}
		}
		// Hot-reload bridge web TTL.
		if bridgeBot != nil {
			bridgeBot.SetWebUserTTL(time.Duration(updated.Bridge.WebUserTTLMinutes) * time.Minute)
		}
		// Regenerate ircd.yaml and rehash Ergo on config changes.
		if ergoMgr != nil {
			if err := ergoMgr.UpdateConfig(updated.Ergo); err != nil {
				log.Error("ergo config hot-reload failed", "err", err)
			} else {
				log.Info("ergo config reloaded")
			}
		}
	})

	// Start HTTP REST API server.
	var llmCfg *config.LLMConfig
	if len(cfg.LLM.Backends) > 0 {
		llmCfg = &cfg.LLM
	}
	// Pass an explicit nil interface when topology is not configured.
	// A nil *topology.Manager passed as a topologyManager interface is
	// non-nil (Go nil interface trap) and causes panics in setAgentModes.
	var topoIface api.TopologyManager
	if topoMgr != nil {
		topoIface = topoMgr
	}
	noAuthMode := os.Getenv("SCUTTLEBOT_NO_AUTH") == "1"
	showToken := os.Getenv("SCUTTLEBOT_SHOW_TOKEN") == "1"
	if noAuthMode && showToken {
		log.Error("SCUTTLEBOT_NO_AUTH and SCUTTLEBOT_SHOW_TOKEN are mutually exclusive — unset one")
		os.Exit(1)
	}
	if noAuthMode {
		log.Warn("no-auth mode enabled (SCUTTLEBOT_NO_AUTH=1) — do not expose this server to untrusted networks")
	}
	if showToken {
		log.Warn("show-token mode enabled (SCUTTLEBOT_SHOW_TOKEN=1) — dev token will be visible in the login UI")
	}
	apiSrv := api.New(reg, apiKeyStore, bridgeBot, policyStore, adminStore, llmCfg, topoIface, cfgStore, ergoMgr.API(), cfg.TLS.Domain, noAuthMode, showToken, log)
	apiSrv.SetBotManager(botMgr)
	// Re-apply ChanServ AMODE for every persisted agent under the current
	// policy so stale +o grants written under older code (e.g. before
	// orchestrators defaulted to +v) get cleared automatically on restart.
	apiSrv.ReconcileAgentModes()
	handler := apiSrv.Handler()

	var httpServer, tlsServer *http.Server

	if cfg.TLS.Domain != "" {
		certDir := cfg.TLS.CertDir
		if certDir == "" {
			certDir = filepath.Join(cfg.Ergo.DataDir, "certs")
		}
		if err := os.MkdirAll(certDir, 0700); err != nil {
			log.Error("create cert dir", "err", err)
			os.Exit(1)
		}

		m := &autocert.Manager{
			Cache:      autocert.DirCache(certDir),
			Prompt:     autocert.AcceptTOS,
			Email:      cfg.TLS.Email,
			HostPolicy: autocert.HostWhitelist(cfg.TLS.Domain),
		}

		// HTTPS on :443
		tlsServer = &http.Server{
			Addr:      ":443",
			Handler:   handler,
			TLSConfig: &tls.Config{GetCertificate: m.GetCertificate},
		}
		go func() {
			log.Info("api server listening (TLS)", "addr", ":443", "domain", cfg.TLS.Domain)
			if err := tlsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				log.Error("tls server error", "err", err)
			}
		}()

		// HTTP on :80 — ACME challenge always enabled; also serves API when AllowInsecure.
		var httpHandler http.Handler
		if cfg.TLS.AllowInsecure {
			httpHandler = m.HTTPHandler(handler)
		} else {
			httpHandler = m.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "https://"+cfg.TLS.Domain+r.RequestURI, http.StatusMovedPermanently)
			}))
		}
		httpServer = &http.Server{Addr: ":80", Handler: httpHandler}
		go func() {
			log.Info("http server listening", "addr", ":80", "insecure", cfg.TLS.AllowInsecure)
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error("http server error", "err", err)
			}
		}()
	} else {
		// No TLS — plain HTTP on configured addr.
		httpServer = &http.Server{
			Addr:    cfg.APIAddr,
			Handler: handler,
		}
		go func() {
			log.Info("api server listening", "addr", httpServer.Addr)
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error("api server error", "err", err)
			}
		}()
	}

	// Start MCP server.
	mcpSrv := mcp.New(reg, &ergoChannelLister{ergoMgr.API()}, apiKeyStore, log)
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
	if httpServer != nil {
		_ = httpServer.Shutdown(shutdownCtx)
	}
	if tlsServer != nil {
		_ = tlsServer.Shutdown(shutdownCtx)
	}
	_ = mcpServer.Shutdown(shutdownCtx)

	log.Info("goodbye")
}

// ergoChannelListAdapter adapts ergo.APIClient to botmanager.ChannelLister.
type ergoChannelListAdapter struct {
	api *ergo.APIClient
}

func (e *ergoChannelListAdapter) ListChannels() ([]string, error) {
	resp, err := e.api.ListChannels()
	if err != nil {
		return nil, err
	}
	out := make([]string, len(resp.Channels))
	for i, ch := range resp.Channels {
		out[i] = ch.Name
	}
	return out, nil
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

// loadOrCreateToken reads a token from path. If the file doesn't exist it
// generates a new token, writes it, and returns it.
func loadOrCreateToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		t := strings.TrimSpace(string(data))
		if t != "" {
			return t, nil
		}
	}
	if !os.IsNotExist(err) && err != nil {
		return "", fmt.Errorf("read token %s: %w", path, err)
	}
	token := mustGenToken()
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		return "", fmt.Errorf("write token %s: %w", path, err)
	}
	return token, nil
}
