package migrate_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/db/migrations"
	"github.com/1yoouoo/mcpaste/internal/database/migrate"
	"github.com/1yoouoo/mcpaste/internal/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
		if len(status.Applied) != 0 || status.Available != 6 {
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
		if len(current.Applied) != 6 || current.Available != 6 {
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
		if len(status.Applied) != 6 || status.Available != 6 || status.Applied[0].Name != "identity" || status.Applied[1].Name != "text_pastes" || status.Applied[2].Name != "image_bundles" || status.Applied[3].Name != "event_cursor_floor" || status.Applied[4].Name != "unified_paste_attachments" || status.Applied[5].Name != "pairing_pending_denial" {
			t.Fatalf("status counts/name = %d/%d/%q", len(status.Applied), status.Available, status.Applied[0].Name)
		}
		if err := migrate.DownOne(ctx, conn, available); err != nil {
			return err
		}
		rolledBack, err := migrate.Status(ctx, conn, available)
		if err != nil {
			return err
		}
		if len(rolledBack.Applied) != 5 || rolledBack.Available != 6 {
			t.Fatalf("rolled-back status counts = %d/%d", len(rolledBack.Applied), rolledBack.Available)
		}
		lastApplied := rolledBack.Applied[len(rolledBack.Applied)-1]
		if lastApplied.Version != 5 || lastApplied.Name != "unified_paste_attachments" {
			t.Fatalf("last applied migration = %06d_%s", lastApplied.Version, lastApplied.Name)
		}
		for _, applied := range rolledBack.Applied {
			if applied.Name == "pairing_pending_denial" {
				t.Fatal("pairing_pending_denial remains applied after rollback")
			}
		}
		var columnName string
		if err := conn.QueryRow(ctx, "select column_name from information_schema.columns where table_schema = current_schema() and table_name = 'workspaces' and column_name = 'event_retention_floor'").Scan(&columnName); err != nil {
			return err
		}
		if columnName != "event_retention_floor" {
			t.Fatalf("event_retention_floor column = %q", columnName)
		}
		if err := migrate.Up(ctx, conn, available); err != nil {
			return err
		}
		reapplied, err := migrate.Status(ctx, conn, available)
		if err != nil {
			return err
		}
		if len(reapplied.Applied) != 6 || reapplied.Available != 6 {
			t.Fatalf("reapplied status counts = %d/%d", len(reapplied.Applied), reapplied.Available)
		}
		if reapplied.Applied[5].Name != "pairing_pending_denial" {
			t.Fatalf("last reapplied migration = %06d_%s", reapplied.Applied[5].Version, reapplied.Applied[5].Name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("migration lifecycle: %v", err)
	}
}

func TestUnifiedPasteAttachmentsMigrationPreservesRowsAndGuardsRollback(t *testing.T) {
	ctx := context.Background()
	pool := testdb.NewUnmigrated(t)
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(available) != 6 {
		t.Fatalf("available migrations = %d, want 6", len(available))
	}
	migration5 := available[:5]

	const (
		workspaceID          = "10000000-0000-4000-8000-000000000001"
		contentPasteID       = "20000000-0000-4000-8000-000000000001"
		imagePasteID         = "20000000-0000-4000-8000-000000000002"
		contentRevisionID    = "30000000-0000-4000-8000-000000000001"
		imageRevisionID      = "30000000-0000-4000-8000-000000000002"
		attachmentRevisionID = "30000000-0000-4000-8000-000000000003"
		contentKeyID         = "content-key"
	)
	contentNonce := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	contentCiphertext := []byte("deterministic-content-ciphertext")
	contentCreatedAt := time.Date(2026, time.January, 15, 10, 0, 0, 0, time.UTC)
	contentExpiresAt := contentCreatedAt.AddDate(1, 0, 0)
	imageCreatedAt := time.Date(2026, time.January, 15, 11, 0, 0, 0, time.UTC)
	imageExpiresAt := imageCreatedAt.Add(24 * time.Hour)
	attachmentCreatedAt := time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)
	attachmentExpiresAt := attachmentCreatedAt.Add(24 * time.Hour)

	err = migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
		if err := migrate.Up(ctx, conn, migration5[:4]); err != nil {
			return err
		}

		if _, err := conn.Exec(ctx, "insert into workspaces(id) values ($1)", workspaceID); err != nil {
			return fmt.Errorf("seed workspace: %w", err)
		}
		if _, err := conn.Exec(ctx, `
insert into pastes(id, workspace_id, paste_kind, created_at)
values
    ($1, $3, 'text', $4),
    ($2, $3, 'image_bundle', $4)`, contentPasteID, imagePasteID, workspaceID, contentCreatedAt); err != nil {
			return fmt.Errorf("seed pastes: %w", err)
		}
		if _, err := conn.Exec(ctx, `
insert into paste_revisions(
    id, workspace_id, paste_id, server_sequence, revision_kind,
    text_key_id, text_nonce, text_ciphertext, created_at, expires_at
)
values
    ($1, $3, $4, 1, 'content', $6, $7, $8, $9, $10),
    ($2, $3, $5, 2, 'image_bundle', null, null, null, $11, $12)`,
			contentRevisionID, imageRevisionID, workspaceID, contentPasteID, imagePasteID,
			contentKeyID, contentNonce, contentCiphertext, contentCreatedAt, contentExpiresAt,
			imageCreatedAt, imageExpiresAt,
		); err != nil {
			return fmt.Errorf("seed legacy revisions: %w", err)
		}

		assertMigrationStatus := func(wantApplied int, wantLastVersion int64, wantLastName string) error {
			status, err := migrate.Status(ctx, conn, migration5)
			if err != nil {
				return err
			}
			if len(status.Applied) != wantApplied || status.Available != 5 {
				return fmt.Errorf("migration status counts = %d/%d, want %d/5", len(status.Applied), status.Available, wantApplied)
			}
			last := status.Applied[len(status.Applied)-1]
			if last.Version != wantLastVersion || last.Name != wantLastName {
				return fmt.Errorf("last applied migration = %06d_%s, want %06d_%s", last.Version, last.Name, wantLastVersion, wantLastName)
			}
			return nil
		}
		assertLegacyRevisions := func() error {
			var count int
			if err := conn.QueryRow(ctx, `
select count(*)
from paste_revisions
where
    (id = $1 and workspace_id = $3 and paste_id = $4 and server_sequence = 1
        and revision_kind = 'content' and text_key_id = $6 and text_nonce = $7
        and text_ciphertext = $8 and created_at = $9 and expires_at = $10)
    or
    (id = $2 and workspace_id = $3 and paste_id = $5 and server_sequence = 2
        and revision_kind = 'image_bundle' and text_key_id is null and text_nonce is null
        and text_ciphertext is null and created_at = $11 and expires_at = $12)`,
				contentRevisionID, imageRevisionID, workspaceID, contentPasteID, imagePasteID,
				contentKeyID, contentNonce, contentCiphertext, contentCreatedAt, contentExpiresAt,
				imageCreatedAt, imageExpiresAt,
			).Scan(&count); err != nil {
				return fmt.Errorf("inspect legacy revisions: %w", err)
			}
			if count != 2 {
				return fmt.Errorf("unchanged legacy revision count = %d, want 2", count)
			}
			return nil
		}
		insertAttachment := func() error {
			_, err := conn.Exec(ctx, `
insert into paste_revisions(
    id, workspace_id, paste_id, server_sequence, revision_kind,
    text_key_id, text_nonce, text_ciphertext, created_at, expires_at
)
values ($1, $2, $3, 3, 'attachment_bundle', null, null, null, $4, $5)`,
				attachmentRevisionID, workspaceID, imagePasteID, attachmentCreatedAt, attachmentExpiresAt,
			)
			return err
		}
		assertAttachmentExists := func() error {
			var count int
			if err := conn.QueryRow(ctx, `
select count(*)
from paste_revisions
where id = $1 and workspace_id = $2 and paste_id = $3 and server_sequence = 3
  and revision_kind = 'attachment_bundle'
  and text_key_id is null and text_nonce is null and text_ciphertext is null
  and created_at = $4 and expires_at = $5`,
				attachmentRevisionID, workspaceID, imagePasteID, attachmentCreatedAt, attachmentExpiresAt,
			).Scan(&count); err != nil {
				return fmt.Errorf("inspect attachment revision: %w", err)
			}
			if count != 1 {
				return fmt.Errorf("attachment revision count = %d, want 1", count)
			}
			return nil
		}

		if err := assertMigrationStatus(4, 4, "event_cursor_floor"); err != nil {
			return err
		}
		if err := migrate.Up(ctx, conn, migration5); err != nil {
			return err
		}
		if err := assertMigrationStatus(5, 5, "unified_paste_attachments"); err != nil {
			return err
		}
		if err := assertLegacyRevisions(); err != nil {
			return err
		}
		if err := insertAttachment(); err != nil {
			return fmt.Errorf("insert attachment revision after migration 5: %w", err)
		}

		downErr := migrate.DownOne(ctx, conn, migration5)
		if downErr == nil || !strings.Contains(downErr.Error(), "cannot rollback migration 000005 while attachment_bundle revisions exist") {
			return fmt.Errorf("guarded DownOne() error = %v", downErr)
		}
		if err := assertMigrationStatus(5, 5, "unified_paste_attachments"); err != nil {
			return err
		}
		if err := assertAttachmentExists(); err != nil {
			return err
		}
		var revisionKindCheck string
		if err := conn.QueryRow(ctx, `
select pg_get_constraintdef(oid)
from pg_constraint
where conrelid = 'paste_revisions'::regclass
  and conname = 'paste_revisions_revision_kind_check'`).Scan(&revisionKindCheck); err != nil {
			return fmt.Errorf("inspect revision kind constraint: %w", err)
		}
		if !strings.Contains(revisionKindCheck, "attachment_bundle") {
			return fmt.Errorf("revision kind constraint after guarded rollback = %q", revisionKindCheck)
		}

		commandTag, err := conn.Exec(ctx, "delete from paste_revisions where id = $1", attachmentRevisionID)
		if err != nil {
			return fmt.Errorf("delete test attachment revision: %w", err)
		}
		if commandTag.RowsAffected() != 1 {
			return fmt.Errorf("deleted attachment revisions = %d, want 1", commandTag.RowsAffected())
		}
		if err := migrate.DownOne(ctx, conn, migration5); err != nil {
			return err
		}
		if err := assertMigrationStatus(4, 4, "event_cursor_floor"); err != nil {
			return err
		}
		if err := insertAttachment(); err == nil {
			return errors.New("attachment_bundle accepted after rolling back migration 5")
		} else {
			var postgresErr *pgconn.PgError
			if !errors.As(err, &postgresErr) || postgresErr.Code != "23514" {
				return fmt.Errorf("attachment_bundle rejection after rollback = %v", err)
			}
			switch postgresErr.ConstraintName {
			case "paste_revisions_revision_kind_check", "paste_revisions_exact_retention", "paste_revisions_body_fields":
			default:
				return fmt.Errorf("attachment_bundle rejected by unexpected constraint %q", postgresErr.ConstraintName)
			}
		}
		if err := assertLegacyRevisions(); err != nil {
			return err
		}

		if err := migrate.Up(ctx, conn, migration5); err != nil {
			return err
		}
		if err := assertMigrationStatus(5, 5, "unified_paste_attachments"); err != nil {
			return err
		}
		if err := insertAttachment(); err != nil {
			return fmt.Errorf("insert attachment revision after reapplying migration 5: %w", err)
		}
		if err := assertLegacyRevisions(); err != nil {
			return err
		}
		return assertAttachmentExists()
	})
	if err != nil {
		t.Fatalf("unified paste attachments migration safety: %v", err)
	}
}

func TestPairingPendingDenialMigrationAllowsPendingInvalidationAndGuardsRollback(t *testing.T) {
	ctx := context.Background()
	pool := testdb.NewUnmigrated(t)
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(available) != 6 {
		t.Fatalf("available migrations = %d, want 6", len(available))
	}

	const pairingID = "60000000-0000-4000-8000-000000000001"
	createdAt := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	expiresAt := createdAt.Add(5 * time.Minute)
	invalidatedAt := createdAt.Add(time.Minute)
	metadataPurgeAt := expiresAt.Add(24 * time.Hour)

	err = migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
		if err := migrate.Up(ctx, conn, available); err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, `
insert into pairing_requests(
    id, short_code, claim_hash, proposed_name, platform, requested_scope,
    created_at, expires_at, metadata_purge_at
) values ($1, '2345678C', $2, 'Pending denial', 'linux', 'connector', $3, $4, $5)`,
			pairingID, bytes.Repeat([]byte{0x61}, 32), createdAt, expiresAt, metadataPurgeAt,
		); err != nil {
			return fmt.Errorf("seed pending pairing: %w", err)
		}
		if _, err := conn.Exec(ctx, `
update pairing_requests
set claim_invalidated_at = $2, metadata_purge_at = greatest(metadata_purge_at, $2::timestamptz + interval '24 hours')
where id = $1::uuid`, pairingID, invalidatedAt); err != nil {
			return fmt.Errorf("invalidate pending pairing after migration 6: %w", err)
		}

		downErr := migrate.DownOne(ctx, conn, available)
		if downErr == nil || !strings.Contains(downErr.Error(), "cannot rollback migration 000006 while pending denied pairings exist") {
			return fmt.Errorf("guarded DownOne() error = %v", downErr)
		}
		status, err := migrate.Status(ctx, conn, available)
		if err != nil {
			return err
		}
		if len(status.Applied) != 6 || status.Applied[5].Name != "pairing_pending_denial" {
			return fmt.Errorf("guarded rollback migration state = %d/%q", len(status.Applied), status.Applied[len(status.Applied)-1].Name)
		}
		var storedInvalidatedAt time.Time
		if err := conn.QueryRow(ctx, `select claim_invalidated_at from pairing_requests where id = $1::uuid`, pairingID).Scan(&storedInvalidatedAt); err != nil {
			return fmt.Errorf("inspect pending denial after guarded rollback: %w", err)
		}
		if !storedInvalidatedAt.Equal(invalidatedAt) {
			return fmt.Errorf("pending denial changed after guarded rollback")
		}

		if _, err := conn.Exec(ctx, `delete from pairing_requests where id = $1::uuid`, pairingID); err != nil {
			return fmt.Errorf("remove guarded rollback fixture: %w", err)
		}
		if err := migrate.DownOne(ctx, conn, available); err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, `
insert into pairing_requests(
    id, short_code, claim_hash, proposed_name, platform, requested_scope,
    created_at, expires_at, metadata_purge_at
) values ($1, '2345678C', $2, 'Pending denial', 'linux', 'connector', $3, $4, $5)`,
			pairingID, bytes.Repeat([]byte{0x62}, 32), createdAt, expiresAt, metadataPurgeAt,
		); err != nil {
			return fmt.Errorf("seed pending pairing after rollback: %w", err)
		}
		if _, err := conn.Exec(ctx, `update pairing_requests set claim_invalidated_at = $2 where id = $1::uuid`, pairingID, invalidatedAt); err == nil {
			return errors.New("pending invalidation accepted after rolling back migration 6")
		} else if constraintErr := new(pgconn.PgError); !errors.As(err, &constraintErr) || constraintErr.ConstraintName != "pairing_invalidation_after_approval" {
			return fmt.Errorf("pending invalidation rollback rejection = %v", err)
		}

		if err := migrate.Up(ctx, conn, available); err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, `update pairing_requests set claim_invalidated_at = $2 where id = $1::uuid`, pairingID, invalidatedAt); err != nil {
			return fmt.Errorf("pending invalidation after reapply: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("pairing pending denial migration safety: %v", err)
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
	if len(status.Applied) != status.Available || status.Available != 6 {
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
