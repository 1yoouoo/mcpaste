package migrate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/1yoouoo/mcpaste/db/migrations"
	"github.com/1yoouoo/mcpaste/internal/database/migrate"
	"github.com/1yoouoo/mcpaste/internal/testdb"
	"github.com/jackc/pgx/v5"
)

func TestStatusReportsPartialAndRequireCurrentRejectsIt(t *testing.T) {
	ctx := context.Background()
	pool := testdb.NewUnmigrated(t)
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	err = migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
		status, err := migrate.Status(ctx, conn, available)
		if err != nil {
			return err
		}
		if len(status.Applied) != 0 || status.Available != 1 {
			t.Fatalf("partial status counts = %d/%d", len(status.Applied), status.Available)
		}
		if _, err := migrate.RequireCurrent(ctx, conn, available); !errors.Is(err, migrate.ErrMigrationsNotCurrent) {
			t.Fatalf("RequireCurrent() partial error = %v", err)
		}
		if err := migrate.Up(ctx, conn, available); err != nil {
			return err
		}
		current, err := migrate.RequireCurrent(ctx, conn, available)
		if err != nil {
			return err
		}
		if len(current.Applied) != 1 || current.Available != 1 {
			t.Fatalf("current status counts = %d/%d", len(current.Applied), current.Available)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("migration status lifecycle: %v", err)
	}
}

func TestUpStatusDownAndReapply(t *testing.T) {
	ctx := context.Background()
	pool := testdb.NewUnmigrated(t)
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	err = migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
		if err := migrate.Up(ctx, conn, available); err != nil {
			return err
		}
		status, err := migrate.Status(ctx, conn, available)
		if err != nil {
			return err
		}
		if len(status.Applied) != 1 || status.Available != 1 || status.Applied[0].Name != "identity" {
			t.Fatalf("status counts/name = %d/%d/%q", len(status.Applied), status.Available, status.Applied[0].Name)
		}
		if err := migrate.DownOne(ctx, conn, available); err != nil {
			return err
		}
		var tableName *string
		if err := conn.QueryRow(ctx, "select to_regclass('workspaces')::text").Scan(&tableName); err != nil {
			return err
		}
		if tableName != nil {
			t.Fatalf("workspaces still exists: %q", *tableName)
		}
		return migrate.Up(ctx, conn, available)
	})
	if err != nil {
		t.Fatalf("migration lifecycle: %v", err)
	}
}

func TestVerifyRejectsChangedAppliedChecksum(t *testing.T) {
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	applied := []migrate.Applied{{
		Version:  available[0].Version,
		Name:     available[0].Name,
		Checksum: strings.Repeat("0", 64),
	}}
	if err := migrate.Verify(available, applied); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Verify() error = %v", err)
	}
}

func TestCheckCurrentSucceedsInsideReadOnlyTransaction(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal("acquire read-only test connection")
	}
	defer conn.Release()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal("begin read-only transaction")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	status, err := migrate.CheckCurrent(ctx, tx, available)
	if err != nil {
		t.Fatalf("CheckCurrent() error = %v", err)
	}
	if len(status.Applied) != status.Available || status.Available != 1 {
		t.Fatalf("read-only current counts = %d/%d", len(status.Applied), status.Available)
	}
}

func TestRequireCurrentRejectsUnknownVersionAndChecksumDrift(t *testing.T) {
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	tests := []struct {
		name      string
		statement string
		argument1 any
		argument2 any
		wantKind  string
	}{
		{
			name: "unknown applied version",
			statement: `
insert into schema_migrations(version, name, checksum)
values ($1, 'unknown', $2)`,
			argument1: available[len(available)-1].Version + 1,
			argument2: strings.Repeat("0", 64),
			wantKind:  "unknown migration versions",
		},
		{
			name:      "checksum drift",
			statement: "update schema_migrations set checksum = $1 where version = $2",
			argument1: strings.Repeat("0", 64),
			argument2: available[0].Version,
			wantKind:  "checksum mismatch",
		},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			ctx := context.Background()
			pool := testdb.New(t)
			if _, err := pool.Exec(ctx, item.statement, item.argument1, item.argument2); err != nil {
				t.Fatal("mutate isolated migration state")
			}
			err := migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
				_, err := migrate.RequireCurrent(ctx, conn, available)
				return err
			})
			if err == nil || !strings.Contains(err.Error(), item.wantKind) {
				t.Fatalf("RequireCurrent rejection metadata: nil=%v expected_kind=%v", err == nil, err != nil && strings.Contains(err.Error(), item.wantKind))
			}
		})
	}
}
