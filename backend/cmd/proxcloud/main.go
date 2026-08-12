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

	"github.com/timkrebs9/proxcloud/backend/internal/auditz"
	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/authz"
	"github.com/timkrebs9/proxcloud/backend/internal/bootstrap"
	"github.com/timkrebs9/proxcloud/backend/internal/config"
	"github.com/timkrebs9/proxcloud/backend/internal/console"
	"github.com/timkrebs9/proxcloud/backend/internal/deploy"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/handlers"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/mail"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/reconciler"
	"github.com/timkrebs9/proxcloud/backend/internal/secrets"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
	"github.com/timkrebs9/proxcloud/backend/internal/version"
)

func main() {
	// Native dev convenience: seed env from the repo-root .env if present.
	// Real deployments (compose, systemd) pass env directly. Loaded
	// individually — godotenv.Load stops at the first missing file.
	_ = godotenv.Load(".env")
	_ = godotenv.Load("../.env")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

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
	sessions := auth.NewSessions(st, !cfg.InsecureCookies, cfg.SessionIdleTTL, cfg.SessionAbsoluteTTL)
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
	authzMW := &authz.Middleware{Store: st, Log: log}
	api := &handlers.Deps{PVE: pve, Log: log, Registry: registry, Broker: broker, Deploy: engine, Store: st, Authz: authzMW,
		Mailer: mailer, FrontendOrigin: cfg.FrontendOrigin, InvitationTTL: cfg.InvitationTTL}
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
	// Stale-pending reservation reclaim (ADR-0012 §2.3): frees quota leaked by a
	// backend that died mid-create. Runs after backfill, stops with bgCtx on shutdown.
	go (&reconciler.Reconciler{
		Store:    st,
		Log:      log,
		Interval: cfg.ReconcilerInterval,
		TTL:      cfg.ReservationTTL,
	}).Run(bgCtx)

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
