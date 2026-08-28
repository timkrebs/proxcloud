// Command proxcloud runs the Proxcloud API server — the only component
// that talks to the Proxmox VE API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // embed the IANA tz database so timezone-aware schedules (ADR-0019) resolve on any base image

	"github.com/joho/godotenv"

	apitypes "github.com/timkrebs9/proxcloud/backend/api/types"

	"github.com/timkrebs9/proxcloud/backend/internal/auditz"
	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/authz"
	"github.com/timkrebs9/proxcloud/backend/internal/bootstrap"
	"github.com/timkrebs9/proxcloud/backend/internal/catalog"
	"github.com/timkrebs9/proxcloud/backend/internal/config"
	"github.com/timkrebs9/proxcloud/backend/internal/console"
	"github.com/timkrebs9/proxcloud/backend/internal/deploy"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/handlers"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/lifecycle"
	"github.com/timkrebs9/proxcloud/backend/internal/mail"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/reconciler"
	"github.com/timkrebs9/proxcloud/backend/internal/scheduler"
	"github.com/timkrebs9/proxcloud/backend/internal/secrets"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
	"github.com/timkrebs9/proxcloud/backend/internal/version"
)

// main dispatches on the first argument so one binary serves three roles in the
// delivery pipeline (ADR-0014): `proxcloud` (no args) runs the API server as it
// always has; `proxcloud migrate` is the one-shot migrator service (apply +
// exit, gating the cutover); `proxcloud seed-smoke` provisions the idempotent
// least-privilege smoke fixture (ADR-0016). The serve path is unchanged.
func main() {
	// Native dev convenience: seed env from the repo-root .env if present.
	// Real deployments (compose, systemd) pass env directly. Loaded
	// individually — godotenv.Load stops at the first missing file.
	_ = godotenv.Load(".env")
	_ = godotenv.Load("../.env")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	// One-shot subcommands exit the process; the default (no/"serve" arg) runs
	// the long-lived server. Unknown commands fail loudly (exit 2) rather than
	// silently booting the server.
	switch subcommand() {
	case "migrate":
		os.Exit(runMigrate(log))
	case "seed-smoke":
		os.Exit(runSeedSmoke(log))
	case "", "serve":
		runServe(log)
	default:
		log.Error("unknown command", "command", os.Args[1], "usage", "proxcloud [serve|migrate|seed-smoke]")
		os.Exit(2)
	}
}

// subcommand returns the first CLI argument, or "" when the binary is invoked
// with none (the serve path).
func subcommand() string {
	if len(os.Args) < 2 {
		return ""
	}
	return os.Args[1]
}

// runServe is the API server boot — byte-for-byte the previous main(): open the
// store, migrate at boot, seed the env admin, wire dependencies, serve, and shut
// down gracefully on a signal. It never returns; failures call os.Exit directly.
func runServe(log *slog.Logger) {
	// Log the build metadata first thing so every boot line-item is attributable
	// to a specific commit. Values are link-time (-ldflags); no secrets.
	bi := version.Info()
	log.Info("proxcloud starting", "commit", bi.Commit, "semver", bi.Semver, "buildTime", bi.BuildTime)

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

	// Secrets-at-rest cipher (ADR-0013 §2): AES-256-GCM over SECRETS_KEY, used to
	// seal the TOTP secret. Fail-closed if the (already-validated) key is rejected.
	cipher, err := secrets.New(cfg.SecretsKey)
	if err != nil {
		log.Error("startup failed", "stage", "secrets-cipher", "err", err)
		os.Exit(1)
	}

	// Outbound mail (ADR-0013 §5): SMTPMailer when SMTP_HOST is set, else the dev
	// LogMailer that prints the accept link to stdout (never through slog).
	var mailer mail.Mailer
	if cfg.SMTPEnabled() {
		mailer = mail.SMTPMailer{
			Host:     cfg.SMTPHost,
			Port:     cfg.SMTPPort,
			User:     cfg.SMTPUsername,
			Pass:     cfg.SMTPPassword,
			From:     cfg.SMTPFrom,
			StartTLS: cfg.SMTPStartTLS,
		}
		log.Info("mailer: smtp", "host", cfg.SMTPHost, "port", cfg.SMTPPort, "starttls", cfg.SMTPStartTLS)
	} else {
		mailer = mail.LogMailer{W: os.Stdout}
		log.Info("mailer: dev log-to-stdout — set SMTP_HOST to send real email")
	}

	// FRONTEND_ORIGIN backs the absolute invitation accept link
	// (FRONTEND_ORIGIN + /invite/{token}). Empty is not a hard config error — the
	// server must still boot in dev, and Owners can always create invites — but the
	// emailed accept link would be relative and unusable, so warn loudly.
	if cfg.FrontendOrigin == "" {
		log.Warn("FRONTEND_ORIGIN is empty — invitation accept links will be relative and unusable until it is set")
	}

	hasher := auth.NewHasher()
	sessions := auth.NewSessions(st, !cfg.InsecureCookies, cfg.TrustProxyHeaders, cfg.SessionIdleTTL, cfg.SessionAbsoluteTTL)
	authHandler := &auth.Handler{
		Sessions: sessions,
		Store:    st,
		Hasher:   hasher,
		Log:      log,
		Limiter:  auth.NewLoginLimiter(),
		// Phase 5 wiring (ADR-0013): consumed by the invitation/TOTP/login-2FA
		// handlers in later chunks; injected here so the seam is real.
		Secrets:           cipher,
		Mailer:            mailer,
		Auditz:            &auditz.Recorder{Store: st, Log: log},
		InvitationTTL:     cfg.InvitationTTL,
		LoginChallengeTTL: cfg.LoginChallengeTTL,
		TOTPIssuer:        cfg.TOTPIssuer,
	}

	pve, err := proxmox.New(cfg)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}

	// Ownership backfill (ADR-0010): claim every pre-existing cluster guest into
	// the default tenant/project BEFORE serving, so a later chunk's scoping
	// enforcement never 404s a guest the platform already runs. Idempotent and
	// best-effort against Proxmox — it only fails startup if the local system of
	// record is broken (a missing default tenant/project is fail-closed).
	backfillCtx, backfillCancel := context.WithTimeout(context.Background(), 60*time.Second)
	if err := bootstrap.BackfillOwnership(backfillCtx, st, pve, log); err != nil {
		backfillCancel()
		log.Error("startup failed", "stage", "ownership-backfill", "err", err)
		os.Exit(1)
	}
	backfillCancel()

	broker := events.NewBroker()
	registry := tasks.NewRegistry()
	engine := deploy.NewEngine(pve, registry, broker, log)
	// Settle create-time ownership reservations without giving deploy a store
	// dependency: the engine calls these after the create step settles.
	engine.Finalize = func(ctx context.Context, ownershipID, upid string) error {
		return st.FinalizeOwnership(ctx, ownershipID, upid)
	}
	engine.Release = func(ctx context.Context, ownershipID string) error {
		return st.ReleaseOwnership(ctx, ownershipID)
	}

	// Service catalog (ADR-0025/0026): off by default. When enabled, load and
	// validate the embedded definitions (fail-fast on a malformed def) and build
	// the SSH/SFTP snippet writer — the only node access beyond the API token —
	// with mandatory host-key verification. The engine gains the writer so a
	// catalog deployment can place its cloud-init before CreateVM.
	var catalogDefs *catalog.Catalog
	if cfg.CatalogEnabled {
		catalogDefs, err = catalog.Load()
		if err != nil {
			log.Error("startup failed", "stage", "catalog-load", "err", err)
			os.Exit(1)
		}
		snippetWriter, err := proxmox.NewSnippetWriter(proxmox.SnippetConfig{
			Host:        cfg.ProxmoxNodeSSHHost,
			User:        cfg.ProxmoxNodeSSHUser,
			KeyPath:     cfg.ProxmoxNodeSSHKeyPath,
			KnownHosts:  cfg.ProxmoxNodeKnownHosts,
			StoragePath: cfg.SnippetStoragePath,
			Log:         log,
		})
		if err != nil {
			log.Error("startup failed", "stage", "snippet-writer", "err", err)
			os.Exit(1)
		}
		engine.Snippets = snippetWriter
		log.Info("service catalog enabled", "services", len(catalogDefs.List()),
			"snippet_datastore", cfg.SnippetDatastore, "ssh_host", cfg.ProxmoxNodeSSHHost)
	} else {
		log.Info("service catalog disabled — set CATALOG_ENABLED=true to enable")
	}

	authzMW := &authz.Middleware{Store: st, Log: log}
	// Auto-shutdown service (ADR-0019): shared by the HTTP schedule handlers (to
	// materialize on edit) and the scheduler (to run the stop/warn/start handlers).
	autoShutdown := &lifecycle.AutoShutdown{
		Store: st, PVE: pve, Registry: registry, Broker: broker, Log: log,
		DefaultGrace: cfg.AutoShutdownDefaultGrace,
	}
	// TTL / ephemeral-resource service (ADR-0020): shared by the HTTP TTL handlers
	// (materialize on edit) and the scheduler (run the warn/expire handlers).
	ttlSvc := &lifecycle.TTL{
		Store: st, PVE: pve, Registry: registry, Broker: broker, Log: log,
		DefaultGrace: cfg.AutoShutdownDefaultGrace,
	}
	api := &handlers.Deps{PVE: pve, Log: log, Registry: registry, Broker: broker, Deploy: engine, Store: st, Authz: authzMW,
		Mailer: mailer, FrontendOrigin: cfg.FrontendOrigin, InvitationTTL: cfg.InvitationTTL,
		AutoShutdown: autoShutdown, AutoShutdownEnabled: cfg.AutoShutdownActive(),
		TTL: ttlSvc, TTLEnabled: cfg.TTLActive(),
		Catalog: catalogDefs, CatalogEnabled: cfg.CatalogEnabled, SnippetDatastore: cfg.SnippetDatastore}
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

	// Background loops: node-metrics poller (idle without SSE subscribers) and
	// the tracked-task watcher. All background workers register in bgWG so
	// shutdown can drain them (cancel-then-join) rather than tearing an
	// at-least-once handler down mid-action.
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()
	var bgWG sync.WaitGroup
	startWorker := func(run func(context.Context)) {
		bgWG.Go(func() { run(bgCtx) })
	}
	startWorker((&events.MetricsPoller{PVE: pve, Broker: broker, Log: log}).Run)
	startWorker((&tasks.Watcher{PVE: pve, Registry: registry, Broker: broker, Log: log}).Run)
	// Stale-pending reservation reclaim (ADR-0012 §2.3): frees quota leaked by a
	// backend that died mid-create. Runs after backfill, stops with bgCtx on shutdown.
	startWorker((&reconciler.Reconciler{
		Store:    st,
		Log:      log,
		Interval: cfg.ReconcilerInterval,
		TTL:      cfg.ReservationTTL,
	}).Run)

	// Job scheduler (ADR-0018): the persistent, tenant-aware engine behind
	// auto-shutdown + TTL. Off by default; handlers are registered by the feature
	// wiring (Part 2/3) only when their own flag is also on. locked_by is unique
	// per instance so a claim is attributable across a blue/green pair.
	if cfg.SchedulerEnabled {
		hostname, _ := os.Hostname()
		sched := &scheduler.Scheduler{
			Store:      st,
			Log:        log,
			Interval:   cfg.SchedulerInterval,
			InstanceID: fmt.Sprintf("%s-%d", hostname, os.Getpid()),
		}
		// Register the auto-shutdown handlers BEFORE Run, only when the feature is
		// active (its own flag AND the scheduler engine). Nothing registers otherwise.
		if cfg.AutoShutdownActive() {
			sched.Register(lifecycle.HandlerStop, autoShutdown.AutoShutdownStop)
			sched.Register(lifecycle.HandlerWarn, autoShutdown.AutoShutdownWarn)
			sched.Register(lifecycle.HandlerStart, autoShutdown.AutoShutdownStart)
		}
		// TTL warn/expire handlers register only when the TTL feature is active (its
		// own flag AND the scheduler engine). Nothing registers otherwise.
		if cfg.TTLActive() {
			sched.Register(lifecycle.HandlerTTLWarn, ttlSvc.TTLWarn)
			sched.Register(lifecycle.HandlerTTLExpire, ttlSvc.TTLExpire)
		}
		startWorker(sched.Run)
		log.Info("scheduler enabled", "interval", cfg.SchedulerInterval.String(),
			"autoshutdown", cfg.AutoShutdownActive(), "ttl", cfg.TTLActive())
	} else {
		log.Info("scheduler disabled — set SCHEDULER_ENABLED=true to enable")
	}

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: httpserver.New(httpserver.Deps{
			Cfg:       cfg,
			Log:       log,
			Auth:      authHandler,
			Health:    api.Health(),
			Events:    events.Handler(broker, log, ownedVMIDsResolver(st)),
			ConsoleWS: consoleWS,
			Authz:     authzMW,
			Account:   api.MountAccount,
			Admin:     api.MountAdmin,
			Tenant:    api.MountTenant,
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
	// Drain background workers: stop accepting new work (bgCancel) then join,
	// bounded, so an in-flight scheduler handler finishes (or is left 'running'
	// for re-claim) instead of being torn down mid-action.
	bgCancel()
	drained := make(chan struct{})
	go func() { bgWG.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(15 * time.Second):
		log.Warn("background workers did not drain in time")
	}
	log.Info("stopped")
}

// ownedVMIDsResolver adapts the store to the SSE handler's per-connection
// owned-VMID query (ADR-0011): the set of VMIDs the tenant owns (active or
// pending), tenant-filtered in SQL.
func ownedVMIDsResolver(st store.Store) events.OwnedVMIDsFunc {
	return func(ctx context.Context, tenantID string) (map[int]bool, error) {
		owns, err := st.ListOwnershipByTenant(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		set := make(map[int]bool, len(owns))
		for _, o := range owns {
			if o.Status == "active" || o.Status == "pending" {
				set[o.VMID] = true
			}
		}
		return set, nil
	}
}
