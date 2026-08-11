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

	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/config"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
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

	passwordHash, err := auth.ResolveHash(cfg.AdminPasswordHash, cfg.AdminPassword)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}
	authHandler := &auth.Handler{
		Sessions:     auth.NewSessions(cfg.SessionSecret, !cfg.Dev),
		AdminUser:    cfg.AdminUser,
		PasswordHash: passwordHash,
		Log:          log,
	}

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           httpserver.New(httpserver.Deps{Cfg: cfg, Log: log, Auth: authHandler}),
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
