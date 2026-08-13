package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/1yoouoo/mcpaste/db/migrations"
	"github.com/1yoouoo/mcpaste/internal/config"
	"github.com/1yoouoo/mcpaste/internal/database"
	"github.com/1yoouoo/mcpaste/internal/database/migrate"
	"github.com/1yoouoo/mcpaste/internal/httpserver"
	"github.com/1yoouoo/mcpaste/internal/identity"
	identitypostgres "github.com/1yoouoo/mcpaste/internal/identity/postgres"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "mcpaste server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadOS()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		return errors.New("load schema migrations")
	}
	if err := requireCurrentSchema(ctx, pool, available); err != nil {
		return err
	}
	keyring, err := secure.ParseKeyring(cfg.ActiveKeyID, cfg.EncryptionKeys, secure.SystemRandom{})
	if err != nil {
		return errors.New("load encryption keyring")
	}
	service := identity.NewService(identitypostgres.New(pool), keyring, secure.SystemRandom{}, identity.RealClock{})
	application := httpserver.NewApplicationHandler(
		databaseReadiness(pool, available),
		service,
		cfg.TrustedProxyCIDRs,
	)
	handler := httpserver.NewRecoveryMiddleware(logger)(httpserver.NewAccessLogMiddleware(logger)(application))
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go runCleanup(ctx, logger, service, cfg.CleanupInterval)
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("server listening", slog.String("address", cfg.HTTPAddr), slog.String("environment", string(cfg.Environment)))
		serverErrors <- server.ListenAndServe()
	}()
	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func requireCurrentSchema(ctx context.Context, pool *pgxpool.Pool, available []migrate.Migration) error {
	err := migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
		_, err := migrate.RequireCurrent(ctx, conn, available)
		return err
	})
	if err != nil {
		return errors.New("database schema is not current")
	}
	return nil
}

const databaseReadinessTimeout = 2 * time.Second

func databaseReadiness(pool *pgxpool.Pool, available []migrate.Migration) httpserver.ReadinessFunc {
	return databaseReadinessWithin(pool, available, databaseReadinessTimeout)
}

func databaseReadinessWithin(pool *pgxpool.Pool, available []migrate.Migration, timeout time.Duration) httpserver.ReadinessFunc {
	return func(ctx context.Context) error {
		checkCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := database.Ready(checkCtx, pool); err != nil {
			return errors.New("database unavailable")
		}
		if _, err := migrate.CheckCurrent(checkCtx, pool, available); err != nil {
			return errors.New("database unavailable")
		}
		return nil
	}
}

func runCleanup(ctx context.Context, logger *slog.Logger, service *identity.Service, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := service.Cleanup(ctx)
			if err != nil {
				logger.Error("identity cleanup failed")
				continue
			}
			logger.Info("identity cleanup complete",
				slog.Int64("revoked_devices", result.RevokedDevices),
				slog.Int64("pairing_rows", result.PairingRows),
				slog.Int64("idempotency_rows", result.IdempotencyRows),
				slog.Int64("event_rows", result.EventRows),
				slog.Int64("rate_limit_rows", result.RateLimitRows),
			)
		}
	}
}
