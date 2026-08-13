package testdb

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/1yoouoo/mcpaste/db/migrations"
	"github.com/1yoouoo/mcpaste/internal/database/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var schemaCounter atomic.Uint64

func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return open(t, true)
}

func NewUnmigrated(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return open(t, false)
}

func open(t *testing.T, apply bool) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("MCPASTE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MCPASTE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal("parse test database URL")
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Fatal("connect to test database")
	}
	schema := fmt.Sprintf("mcpaste_test_%d_%d", os.Getpid(), schemaCounter.Add(1))
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "create schema "+identifier); err != nil {
		admin.Close()
		t.Fatalf("create isolated schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal("parse isolated database URL")
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close()
		t.Fatal("open isolated database pool")
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "drop schema "+identifier+" cascade")
		admin.Close()
	})
	if apply {
		available, err := migrate.Load(migrations.Files)
		if err != nil {
			t.Fatalf("load migrations: %v", err)
		}
		if err := migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
			return migrate.Up(ctx, conn, available)
		}); err != nil {
			t.Fatalf("apply migrations: %v", err)
		}
	}
	return pool
}
