package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/db/migrations"
	"github.com/1yoouoo/mcpaste/internal/database/migrate"
	"github.com/1yoouoo/mcpaste/internal/httpserver"
	"github.com/1yoouoo/mcpaste/internal/identity"
	identitypostgres "github.com/1yoouoo/mcpaste/internal/identity/postgres"
	"github.com/1yoouoo/mcpaste/internal/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestShutdownServerWithinForceClosesActiveRequest(t *testing.T) {
	if serverShutdownTimeout != 10*time.Second {
		t.Fatalf("production shutdown timeout = %v", serverShutdownTimeout)
	}
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-request.Context().Done()
		close(requestCanceled)
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	requestDone := make(chan error, 1)
	go func() {
		response, requestErr := client.Get("http://" + listener.Addr().String())
		if response != nil {
			_ = response.Body.Close()
		}
		requestDone <- requestErr
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("blocking request did not start")
	}

	started := time.Now()
	err = shutdownServerWithin(server, 25*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("forced shutdown exceeded bound: %v", elapsed)
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("forced shutdown did not cancel request context")
	}
	select {
	case serveErr := <-serveDone:
		if !errors.Is(serveErr, http.ErrServerClosed) {
			t.Fatalf("Serve() error = %v", serveErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not return after forced shutdown")
	}
	select {
	case requestErr := <-requestDone:
		if requestErr == nil {
			t.Fatal("forced shutdown left blocking request connected")
		}
	case <-time.After(time.Second):
		t.Fatal("blocking client did not return after forced shutdown")
	}
}

func TestCleanupWorkerStopsOnCancellationBeforePoolClose(t *testing.T) {
	pool := testdb.New(t)
	service := identity.NewService(identitypostgres.New(pool), nil, nil, identity.RealClock{})
	ctx, cancel := context.WithCancel(context.Background())
	cleanupDone := startCleanup(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), service, time.Hour)
	cancel()
	select {
	case <-cleanupDone:
	case <-time.After(time.Second):
		t.Fatal("cleanup worker did not stop after cancellation")
	}
	pingCtx, pingCancel := context.WithTimeout(context.Background(), time.Second)
	defer pingCancel()
	if err := pool.Ping(pingCtx); err != nil {
		t.Fatalf("pool closed before cleanup worker exited: %v", err)
	}
}

func TestStartupAndReadinessRequireCurrentSchema(t *testing.T) {
	ctx := context.Background()
	pool := testdb.NewUnmigrated(t)
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := requireCurrentSchema(ctx, pool, available); err == nil || err.Error() != "database schema is not current" {
		t.Fatalf("startup schema error metadata: nil=%v", err == nil)
	}
	handler := httpserver.NewHandler(databaseReadiness(pool, available))
	unmigrated := httptest.NewRecorder()
	handler.ServeHTTP(unmigrated, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if unmigrated.Code != http.StatusServiceUnavailable || unmigrated.Body.String() != "{\"status\":\"unavailable\"}\n" {
		t.Fatalf("unmigrated readiness metadata = %d/%d", unmigrated.Code, unmigrated.Body.Len())
	}
	if err := migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
		return migrate.Up(ctx, conn, available)
	}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if err := requireCurrentSchema(ctx, pool, available); err != nil {
		t.Fatalf("current startup schema error = %v", err)
	}
	current := httptest.NewRecorder()
	handler.ServeHTTP(current, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if current.Code != http.StatusOK || current.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("current readiness metadata = %d/%d", current.Code, current.Body.Len())
	}
}

func TestStartupAndReadinessRejectUnknownVersionAndChecksumDrift(t *testing.T) {
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	tests := []struct {
		name      string
		statement string
		argument1 any
		argument2 any
	}{
		{
			name: "unknown applied version",
			statement: `
insert into schema_migrations(version, name, checksum)
values ($1, 'unknown', $2)`,
			argument1: available[len(available)-1].Version + 1,
			argument2: strings.Repeat("0", 64),
		},
		{
			name:      "checksum drift",
			statement: "update schema_migrations set checksum = $1 where version = $2",
			argument1: strings.Repeat("0", 64),
			argument2: available[0].Version,
		},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			ctx := context.Background()
			pool := testdb.New(t)
			if _, err := pool.Exec(ctx, item.statement, item.argument1, item.argument2); err != nil {
				t.Fatal("mutate isolated migration state")
			}

			startupErr := requireCurrentSchema(ctx, pool, available)
			if startupErr == nil || startupErr.Error() != "database schema is not current" {
				t.Fatalf("startup rejection metadata: nil=%v generic=%v", startupErr == nil, startupErr != nil && startupErr.Error() == "database schema is not current")
			}

			readiness := databaseReadiness(pool, available)
			readinessErr := readiness(ctx)
			if readinessErr == nil || readinessErr.Error() != "database unavailable" {
				t.Fatalf("readiness closure metadata: nil=%v generic=%v", readinessErr == nil, readinessErr != nil && readinessErr.Error() == "database unavailable")
			}
			handler := httpserver.NewHandler(readiness)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if response.Code != http.StatusServiceUnavailable || response.Body.String() != "{\"status\":\"unavailable\"}\n" {
				t.Fatalf("readyz rejection metadata = %d/%d", response.Code, response.Body.Len())
			}

			for markerIndex, marker := range []string{
				"postgres://", "schema_migrations", "checksum", strings.Repeat("0", 64),
			} {
				if strings.Contains(startupErr.Error(), marker) || strings.Contains(readinessErr.Error(), marker) || strings.Contains(response.Body.String(), marker) {
					t.Fatalf("migration rejection leaked marker index %d", markerIndex)
				}
			}
		})
	}
}

func TestReadinessDoesNotWaitForMigrationLockAndIsBounded(t *testing.T) {
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	t.Run("migration advisory lock does not block readiness", func(t *testing.T) {
		pool := testdb.New(t)
		lockHeld := make(chan struct{})
		releaseLock := make(chan struct{})
		lockResult := make(chan error, 1)
		go func() {
			lockResult <- migrate.WithLock(context.Background(), pool, func(*pgx.Conn) error {
				close(lockHeld)
				<-releaseLock
				return nil
			})
		}()
		select {
		case <-lockHeld:
		case <-time.After(2 * time.Second):
			t.Fatal("migration advisory lock was not acquired")
		}
		readinessCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if err := databaseReadiness(pool, available)(readinessCtx); err != nil {
			close(releaseLock)
			<-lockResult
			t.Fatal("readiness waited for migration advisory lock")
		}
		close(releaseLock)
		if err := <-lockResult; err != nil {
			t.Fatal("migration lock holder returned an error")
		}
	})

	t.Run("pool starvation is bounded", func(t *testing.T) {
		if databaseReadinessTimeout != 2*time.Second {
			t.Fatalf("readiness timeout = %v", databaseReadinessTimeout)
		}
		pool := testdb.New(t)
		held := make([]*pgxpool.Conn, 0, pool.Config().MaxConns)
		for index := int32(0); index < pool.Config().MaxConns; index++ {
			conn, err := pool.Acquire(context.Background())
			if err != nil {
				t.Fatal("acquire pool-starvation connection")
			}
			held = append(held, conn)
		}
		defer func() {
			for _, conn := range held {
				conn.Release()
			}
		}()
		started := time.Now()
		err := databaseReadinessWithin(pool, available, 50*time.Millisecond)(context.Background())
		if err == nil || err.Error() != "database unavailable" {
			t.Fatal("pool-starved readiness did not fail generically")
		}
		if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
			t.Fatalf("pool-starved readiness exceeded bound: %v", elapsed)
		}
	})
}
