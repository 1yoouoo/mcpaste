package migrate_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/db/migrations"
	"github.com/1yoouoo/mcpaste/internal/database/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIdentitySchemaContract(t *testing.T) {
	databaseURL := os.Getenv("MCPASTE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MCPASTE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal("parse test database URL")
	}
	defer admin.Close()
	if err := admin.Ping(ctx); err != nil {
		t.Fatal("connect to test database")
	}

	schema := fmt.Sprintf("mcpaste_schema_contract_%d_%d", os.Getpid(), time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "create schema "+identifier); err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}
	defer func() {
		_, _ = admin.Exec(context.Background(), "drop schema "+identifier+" cascade")
	}()

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("parse isolated database URL")
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("open isolated database pool")
	}
	defer pool.Close()

	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
		return migrate.Up(ctx, conn, available)
	}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	wantTables := []string{
		"credentials",
		"devices",
		"idempotency_records",
		"pairing_requests",
		"rate_limit_buckets",
		"recovery_verifiers",
		"workspace_events",
		"workspaces",
	}
	for _, table := range wantTables {
		var count int
		if err := pool.QueryRow(ctx, `
select count(*)
from information_schema.tables
where table_schema = $1 and table_name = $2`, schema, table).Scan(&count); err != nil {
			t.Fatalf("inspect table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", table, count)
		}
	}

	var claimInvalidatedType string
	if err := pool.QueryRow(ctx, `
select data_type
from information_schema.columns
where table_schema = $1
  and table_name = 'pairing_requests'
  and column_name = 'claim_invalidated_at'`, schema).Scan(&claimInvalidatedType); err != nil {
		t.Fatalf("inspect claim_invalidated_at: %v", err)
	}
	if claimInvalidatedType != "timestamp with time zone" {
		t.Fatalf("claim_invalidated_at type = %q", claimInvalidatedType)
	}

	var idempotencyPrimaryKey string
	if err := pool.QueryRow(ctx, `
select pg_get_constraintdef(oid)
from pg_constraint
where conrelid = to_regclass($1) and contype = 'p'`, schema+".idempotency_records").Scan(&idempotencyPrimaryKey); err != nil {
		t.Fatalf("inspect idempotency primary key: %v", err)
	}
	if idempotencyPrimaryKey != "PRIMARY KEY (scope_id, operation, key_hash)" {
		t.Fatalf("idempotency primary key = %q", idempotencyPrimaryKey)
	}
	var createdAtDefault *string
	if err := pool.QueryRow(ctx, `
select column_default
from information_schema.columns
where table_schema = $1
  and table_name = 'idempotency_records'
  and column_name = 'created_at'`, schema).Scan(&createdAtDefault); err != nil {
		t.Fatalf("inspect idempotency created_at: %v", err)
	}
	if createdAtDefault != nil {
		t.Fatal("idempotency created_at has an unexpected default")
	}

	for _, index := range []string{
		"credentials_active_lookup_index",
		"devices_workspace_display_name_ci_unique",
		"idempotency_expiry_index",
		"idempotency_scope_workspace_expiry_index",
		"pairing_claim_expiry_index",
		"pairing_metadata_purge_index",
		"rate_limit_expiry_index",
		"workspace_events_expiry_index",
	} {
		var count int
		if err := pool.QueryRow(ctx, `
select count(*)
from pg_indexes
where schemaname = $1 and indexname = $2`, schema, index).Scan(&count); err != nil {
			t.Fatalf("inspect index %s: %v", index, err)
		}
		if count != 1 {
			t.Fatalf("index %s count = %d, want 1", index, count)
		}
	}
}
