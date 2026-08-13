package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const lockName = "mcpaste-schema-migrations-v1"

const lockCleanupTimeout = 5 * time.Second

var ErrMigrationsNotCurrent = errors.New("database migrations are not current")

type Migration struct {
	Version  int64
	Name     string
	Checksum string
	Up       string
	Down     string
}

type Applied struct {
	Version  int64
	Name     string
	Checksum string
}

type MigrationStatus struct {
	Applied   []Applied
	Available int
}

type migrationQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type migrationLockSession interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Release()
	Dispose(context.Context) error
}

type pooledMigrationLockSession struct {
	conn *pgxpool.Conn
}

func (s pooledMigrationLockSession) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return s.conn.QueryRow(ctx, sql, args...)
}

func (s pooledMigrationLockSession) Release() {
	s.conn.Release()
}

func (s pooledMigrationLockSession) Dispose(ctx context.Context) error {
	return s.conn.Hijack().Close(ctx)
}

func Load(source fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	type pair struct {
		name string
		up   []byte
		down []byte
	}
	pairs := make(map[int64]*pair)
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "embed.go" {
			continue
		}
		version, name, direction, err := parseName(entry.Name())
		if err != nil {
			return nil, err
		}
		body, err := fs.ReadFile(source, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		current := pairs[version]
		if current == nil {
			current = &pair{name: name}
			pairs[version] = current
		}
		if current.name != name {
			return nil, fmt.Errorf("migration version %06d has conflicting names", version)
		}
		switch direction {
		case "up":
			if current.up != nil {
				return nil, fmt.Errorf("migration version %06d has duplicate up file", version)
			}
			current.up = body
		case "down":
			if current.down != nil {
				return nil, fmt.Errorf("migration version %06d has duplicate down file", version)
			}
			current.down = body
		}
	}
	versions := make([]int64, 0, len(pairs))
	for version := range pairs {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	result := make([]Migration, 0, len(versions))
	for index, version := range versions {
		if version != int64(index+1) {
			return nil, fmt.Errorf("migration sequence gap before version %06d", version)
		}
		current := pairs[version]
		if current.up == nil || current.down == nil {
			return nil, fmt.Errorf("migration version %06d must have up and down files", version)
		}
		sum := sha256.Sum256(current.up)
		result = append(result, Migration{
			Version:  version,
			Name:     current.name,
			Checksum: hex.EncodeToString(sum[:]),
			Up:       string(current.up),
			Down:     string(current.down),
		})
	}
	return result, nil
}

func parseName(value string) (int64, string, string, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[2] != "sql" || (parts[1] != "up" && parts[1] != "down") {
		return 0, "", "", fmt.Errorf("invalid migration filename %q", value)
	}
	prefix := strings.SplitN(parts[0], "_", 2)
	if len(prefix) != 2 || len(prefix[0]) != 6 || prefix[1] == "" {
		return 0, "", "", fmt.Errorf("invalid migration filename %q", value)
	}
	for _, r := range prefix[0] {
		if r < '0' || r > '9' {
			return 0, "", "", fmt.Errorf("invalid migration filename %q", value)
		}
	}
	version, err := strconv.ParseInt(prefix[0], 10, 64)
	if err != nil || version < 1 {
		return 0, "", "", fmt.Errorf("invalid migration filename %q", value)
	}
	for _, r := range prefix[1] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return 0, "", "", fmt.Errorf("invalid migration filename %q", value)
		}
	}
	return version, prefix[1], parts[1], nil
}

func WithLock(ctx context.Context, pool *pgxpool.Pool, fn func(*pgx.Conn) error) (resultErr error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	locked := false
	defer func() {
		if !locked {
			conn.Release()
			return
		}
		resultErr = finishMigrationLock(pooledMigrationLockSession{conn: conn}, resultErr)
	}()
	if _, err := conn.Exec(ctx, "select pg_advisory_lock(hashtextextended($1, 0))", lockName); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	locked = true
	return fn(conn.Conn())
}

func finishMigrationLock(session migrationLockSession, operationErr error) error {
	return errors.Join(operationErr, cleanupMigrationLock(session))
}

func cleanupMigrationLock(session migrationLockSession) error {
	unlockCtx, cancelUnlock := context.WithTimeout(context.Background(), lockCleanupTimeout)
	var unlocked bool
	err := session.QueryRow(unlockCtx, "select pg_advisory_unlock(hashtextextended($1, 0))", lockName).Scan(&unlocked)
	cancelUnlock()
	if err == nil && unlocked {
		session.Release()
		return nil
	}

	cleanupErr := errors.New("unlock migrations")
	closeCtx, cancelClose := context.WithTimeout(context.Background(), lockCleanupTimeout)
	defer cancelClose()
	if err := session.Dispose(closeCtx); err != nil {
		return errors.Join(cleanupErr, errors.New("close migration connection"))
	}
	return cleanupErr
}

func EnsureTable(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, `
create table if not exists schema_migrations (
    version bigint primary key,
    name text not null,
    checksum char(64) not null check (checksum ~ '^[0-9a-f]{64}$'),
    applied_at timestamptz not null default transaction_timestamp()
)`)
	if err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	return nil
}

func Status(ctx context.Context, conn *pgx.Conn, available []Migration) (MigrationStatus, error) {
	if err := EnsureTable(ctx, conn); err != nil {
		return MigrationStatus{}, err
	}
	applied, err := readApplied(ctx, conn)
	if err != nil {
		return MigrationStatus{}, err
	}
	if err := Verify(available, applied); err != nil {
		return MigrationStatus{}, err
	}
	return MigrationStatus{Applied: applied, Available: len(available)}, nil
}

func CheckCurrent(ctx context.Context, queryer migrationQuerier, available []Migration) (MigrationStatus, error) {
	if len(available) == 0 {
		return MigrationStatus{}, ErrMigrationsNotCurrent
	}
	applied, err := readApplied(ctx, queryer)
	if err != nil {
		return MigrationStatus{}, err
	}
	if err := Verify(available, applied); err != nil {
		return MigrationStatus{}, err
	}
	status := MigrationStatus{Applied: applied, Available: len(available)}
	if len(status.Applied) != status.Available {
		return status, ErrMigrationsNotCurrent
	}
	return status, nil
}

func readApplied(ctx context.Context, queryer migrationQuerier) ([]Applied, error) {
	rows, err := queryer.Query(ctx, "select version, name, checksum from schema_migrations order by version")
	if err != nil {
		return nil, fmt.Errorf("query migration status: %w", err)
	}
	defer rows.Close()
	applied := make([]Applied, 0)
	for rows.Next() {
		var item Applied
		if err := rows.Scan(&item.Version, &item.Name, &item.Checksum); err != nil {
			return nil, fmt.Errorf("scan migration status: %w", err)
		}
		applied = append(applied, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration status: %w", err)
	}
	return applied, nil
}

func RequireCurrent(ctx context.Context, conn *pgx.Conn, available []Migration) (MigrationStatus, error) {
	if len(available) == 0 {
		return MigrationStatus{}, ErrMigrationsNotCurrent
	}
	status, err := Status(ctx, conn, available)
	if err != nil {
		return MigrationStatus{}, err
	}
	if len(status.Applied) != status.Available {
		return status, ErrMigrationsNotCurrent
	}
	return status, nil
}

func Verify(available []Migration, applied []Applied) error {
	if len(applied) > len(available) {
		return errors.New("database contains unknown migration versions")
	}
	for index, got := range applied {
		want := available[index]
		if got.Version != want.Version || got.Name != want.Name {
			return fmt.Errorf("database migration sequence differs at version %06d", got.Version)
		}
		if got.Checksum != want.Checksum {
			return fmt.Errorf("migration checksum mismatch at version %06d", got.Version)
		}
	}
	return nil
}

func Up(ctx context.Context, conn *pgx.Conn, available []Migration) error {
	status, err := Status(ctx, conn, available)
	if err != nil {
		return err
	}
	for _, migration := range available[len(status.Applied):] {
		if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, migration.Up); err != nil {
				return fmt.Errorf("apply migration %06d_%s: %w", migration.Version, migration.Name, err)
			}
			_, err := tx.Exec(ctx,
				"insert into schema_migrations(version, name, checksum) values ($1, $2, $3)",
				migration.Version, migration.Name, migration.Checksum,
			)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func DownOne(ctx context.Context, conn *pgx.Conn, available []Migration) error {
	status, err := Status(ctx, conn, available)
	if err != nil {
		return err
	}
	if len(status.Applied) == 0 {
		return errors.New("database has no applied migration")
	}
	migration := available[len(status.Applied)-1]
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, migration.Down); err != nil {
			return fmt.Errorf("roll back migration %06d_%s: %w", migration.Version, migration.Name, err)
		}
		_, err := tx.Exec(ctx, "delete from schema_migrations where version = $1", migration.Version)
		return err
	})
}
