// Command proxcloud runs the Proxcloud API server — the only component
// that talks to the Proxmox VE API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	apitypes "github.com/timkrebs9/proxcloud/backend/api/types"

	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/config"
	"github.com/timkrebs9/proxcloud/backend/internal/console"
	"github.com/timkrebs9/proxcloud/backend/internal/deploy"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/handlers"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
)

func main() {
	// Native dev convenience: seed env from the repo-root .env if present.
	// Real deployments (compose, systemd) pass env directly. Loaded
	// individually — godotenv.Load stops at the first missing file.
	_ = godotenv.Load(".env")
	_ = godotenv.Load("../.env")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	cfg, err := config.Load()
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}

	// Datastore: open the pool and apply migrations before serving. Fail fast
	// (fail-closed) if Postgres is unreachable or the schema can't be built —
	// the server never serves on a half-built schema. The pool is closed on
	// graceful shutdown below.
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 30*time.Second)
	st, err := store.New(dbCtx, cfg.DatabaseURL)
	if err != nil {
		dbCancel()
		log.Error("startup failed", "stage", "datastore", "err", err)
		os.Exit(1)
	}
	dbCancel()
	defer st.Close()
	version, err := st.RunMigrations()
	if err != nil {
		log.Error("startup failed", "stage", "migrations", "err", err)
		os.Exit(1)
	}
	log.Info("migrations applied", "version", version)

	// Env-admin cutover (ADR-0006): on a fresh users table with ADMIN_* set,
	// seed exactly one platform-admin DB user; thereafter ADMIN_* is inert and
	// login is email+DB only. Idempotent; logs loudly at WARN.
	seedCtx, seedCancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := auth.SeedEnvAdmin(seedCtx, st, cfg.AdminUser, cfg.AdminPasswordHash, cfg.AdminPassword, log); err != nil {
		seedCancel()
		log.Error("startup failed", "stage", "env-admin-seed", "err", err)
		os.Exit(1)
	}
	seedCancel()

	hasher := auth.NewHasher()
	sessions := auth.NewSessions(st, !cfg.InsecureCookies, cfg.SessionIdleTTL, cfg.SessionAbsoluteTTL)
	authHandler := &auth.Handler{
		Sessions: sessions,
		Store:    st,
		Hasher:   hasher,
		Log:      log,
		Limiter:  auth.NewLoginLimiter(),
	}

	pve, err := proxmox.New(cfg)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}

	broker := events.NewBroker()
	registry := tasks.NewRegistry()
	engine := deploy.NewEngine(pve, registry, broker, log)
	api := &handlers.Deps{PVE: pve, Log: log, Registry: registry, Broker: broker, Deploy: engine, Store: st}
	if cfg.PricingEnabled() {
		currency := cfg.PricingCurrency
		if currency == "" {
			currency = "EUR"
		}
		api.Pricing = &apitypes.Pricing{
			Enabled:     true,
			Currency:    currency,
			VCPUMonth:   cfg.PricingVCPUMonth,
			RAMGBMonth:  cfg.PricingRAMGBMonth,
			DiskGBMonth: cfg.PricingDiskGBMonth,
		}
	}

	// Console: optional credential path (PVE websockets reject API tokens).
	var consoleWS http.Handler
	if consoleAuth := console.NewTicketAuth(cfg); consoleAuth != nil {
		sessions := console.NewSessions()
		api.ConsoleAuth = consoleAuth
		api.ConsoleSessions = sessions
		api.ConsoleUser = cfg.ConsoleUser
		consoleWS = &console.Proxy{
			Auth:           consoleAuth,
			Sessions:       sessions,
			Log:            log,
			FrontendOrigin: cfg.FrontendOrigin,
			// Advisory: a portal cookie, if the browser attaches one, must be
			// valid — but it is not required. The dev console runs over ws://
			// while the session cookie is Secure, so browsers legitimately omit
			// it; the one-shot session id (single-use, 25s, minted only from an
			// authenticated POST /console) is the real per-connection credential.
			VerifyCookie: func(r *http.Request) bool {
				if _, err := r.Cookie(auth.CookieName); err != nil {
					return true // no portal cookie on this transport — rely on the one-shot id
				}
				_, err := authHandler.Sessions.Verify(r.Context(), r)
				return err == nil // a presented cookie must be genuine (rejects forged/stale)
			},
		}
		log.Info("console enabled", "user", cfg.ConsoleUser)
	} else {
		log.Info("console disabled — set PROXMOX_CONSOLE_USER/PASSWORD to enable")
	}

	// Background loops: node-metrics poller (idle without SSE subscribers)
	// and the tracked-task watcher. Both stop on shutdown via bgCtx.
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()
	go (&events.MetricsPoller{PVE: pve, Broker: broker, Log: log}).Run(bgCtx)
	go (&tasks.Watcher{PVE: pve, Registry: registry, Broker: broker, Log: log}).Run(bgCtx)

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: httpserver.New(httpserver.Deps{
			Cfg:       cfg,
			Log:       log,
			Auth:      authHandler,
			Health:    api.Health(),
			Events:    events.Handler(broker, log),
			ConsoleWS: consoleWS,
			Protected: api.Mount,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("proxcloud api listening", "addr", cfg.ListenAddr, "proxmox", cfg.ProxmoxURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("shutdown", "err", err)
	}
	log.Info("stopped")
}
