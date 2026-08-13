package migrate

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fakeMigrationLockRow struct {
	unlocked bool
	err      error
}

func (r fakeMigrationLockRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*bool)) = r.unlocked
	return nil
}

type fakeMigrationLockSession struct {
	unlocked       bool
	unlockErr      error
	disposeErr     error
	released       bool
	disposed       bool
	unlockBounded  bool
	disposeBounded bool
}

func (s *fakeMigrationLockSession) QueryRow(ctx context.Context, _ string, _ ...any) pgx.Row {
	_, s.unlockBounded = ctx.Deadline()
	return fakeMigrationLockRow{unlocked: s.unlocked, err: s.unlockErr}
}

func (s *fakeMigrationLockSession) Release() {
	s.released = true
}

func (s *fakeMigrationLockSession) Dispose(ctx context.Context) error {
	_, s.disposeBounded = ctx.Deadline()
	s.disposed = true
	return s.disposeErr
}

func TestFinishMigrationLockReleasesAfterConfirmedUnlock(t *testing.T) {
	session := &fakeMigrationLockSession{unlocked: true}

	if err := finishMigrationLock(session, nil); err != nil {
		t.Fatalf("finishMigrationLock() error = %v", err)
	}
	if !session.unlockBounded || !session.released || session.disposed {
		t.Fatalf("session cleanup = %#v", session)
	}
}

func TestFinishMigrationLockDisposesAfterUnlockFailureAndPreservesOperationError(t *testing.T) {
	operationErr := errors.New("migration operation failed")
	tests := map[string]struct {
		session   *fakeMigrationLockSession
		wantClose bool
	}{
		"query error": {
			session: &fakeMigrationLockSession{
				unlockErr: errors.New("postgres://secret-in-query-error"),
			},
		},
		"false result": {
			session: &fakeMigrationLockSession{},
		},
		"close error": {
			session: &fakeMigrationLockSession{
				disposeErr: errors.New("postgres://secret-in-close-error"),
			},
			wantClose: true,
		},
	}
	for name, item := range tests {
		t.Run(name, func(t *testing.T) {
			session := item.session
			err := finishMigrationLock(session, operationErr)
			if !errors.Is(err, operationErr) {
				t.Fatalf("finishMigrationLock() error = %v", err)
			}
			if !strings.Contains(err.Error(), "unlock migrations") {
				t.Fatalf("finishMigrationLock() error = %v", err)
			}
			if strings.Contains(err.Error(), "postgres://") {
				t.Fatalf("finishMigrationLock() leaked connection data: %v", err)
			}
			if got := strings.Contains(err.Error(), "close migration connection"); got != item.wantClose {
				t.Fatalf("finishMigrationLock() close error = %v", err)
			}
			if !session.unlockBounded || session.released || !session.disposed || !session.disposeBounded {
				t.Fatalf("session cleanup = %#v", session)
			}
		})
	}
}

func TestWithLockLeavesAdvisoryLockAvailableFromAnotherSession(t *testing.T) {
	databaseURL := os.Getenv("MCPASTE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MCPASTE_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("parse test database URL")
	}
	config.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("open test database pool")
	}
	defer pool.Close()

	verifier, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal("acquire verifier connection")
	}
	defer verifier.Release()

	if err := WithLock(ctx, pool, func(*pgx.Conn) error { return nil }); err != nil {
		t.Fatalf("WithLock() error = %v", err)
	}
	var locked bool
	if err := verifier.QueryRow(ctx, "select pg_try_advisory_lock(hashtextextended($1, 0))", lockName).Scan(&locked); err != nil {
		t.Fatal("try migration lock")
	}
	if !locked {
		t.Fatal("migration advisory lock remains held")
	}
	var unlocked bool
	if err := verifier.QueryRow(ctx, "select pg_advisory_unlock(hashtextextended($1, 0))", lockName).Scan(&unlocked); err != nil || !unlocked {
		t.Fatal("release verifier migration lock")
	}
}

func TestLoadOrdersPairsAndChecksumsUpBytes(t *testing.T) {
	files := fstest.MapFS{
		"000002_second.up.sql":   {Data: []byte("select 2;\n")},
		"000001_first.down.sql":  {Data: []byte("select -1;\n")},
		"000001_first.up.sql":    {Data: []byte("select 1;\n")},
		"000002_second.down.sql": {Data: []byte("select -2;\n")},
	}

	got, err := Load(files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got) != 2 || got[0].Version != 1 || got[1].Version != 2 {
		t.Fatalf("versions = %#v", got)
	}
	if got[0].Name != "first" || got[0].Checksum != "4a45092ccf992ea92250053a80b931b787924ba61648f420555511b84f10ab6c" {
		t.Fatalf("first migration = %#v", got[0])
	}
}

func TestLoadRejectsInvalidSets(t *testing.T) {
	tests := map[string]struct {
		files fstest.MapFS
		want  string
	}{
		"missing down": {
			files: fstest.MapFS{
				"000001_first.up.sql": {Data: []byte("select 1")},
			},
			want: "migration version 000001 must have up and down files",
		},
		"gap": {
			files: fstest.MapFS{
				"000002_second.up.sql":   {Data: []byte("select 2")},
				"000002_second.down.sql": {Data: []byte("select -2")},
			},
			want: "migration sequence gap before version 000002",
		},
		"bad name": {
			files: fstest.MapFS{
				"1_first.up.sql": {Data: []byte("select 1")},
			},
			want: `invalid migration filename "1_first.up.sql"`,
		},
		"signed version": {
			files: fstest.MapFS{
				"+00001_identity.up.sql": {Data: []byte("select 1")},
			},
			want: `invalid migration filename "+00001_identity.up.sql"`,
		},
		"duplicate version with distinct basenames": {
			files: fstest.MapFS{
				"000001_first.up.sql":     {Data: []byte("select 1")},
				"000001_first.down.sql":   {Data: []byte("select -1")},
				"000001_another.up.sql":   {Data: []byte("select 2")},
				"000001_another.down.sql": {Data: []byte("select -2")},
			},
			want: "migration version 000001 has conflicting names",
		},
	}
	for name, item := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(item.files)
			if err == nil || err.Error() != item.want {
				t.Fatalf("Load() exact error match = %v", err != nil && err.Error() == item.want)
			}
		})
	}
}

func TestRequireCurrentRejectsZeroAvailableBeforeDatabaseAccess(t *testing.T) {
	_, err := RequireCurrent(context.Background(), nil, nil)
	if !errors.Is(err, ErrMigrationsNotCurrent) {
		t.Fatalf("RequireCurrent() error = %v", err)
	}
}
