package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/1yoouoo/mcpaste/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

const workspaceOne = "00000000-0000-4000-8000-000000000101"
const workspaceTwo = "00000000-0000-4000-8000-000000000102"
const deviceOne = "00000000-0000-4000-8000-000000000201"

func TestWorkspaceScopedCredentialAuthentication(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	hash := bytes.Repeat([]byte{0x41}, 32)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		device, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now,
		})
		if err != nil || device.DisplayName != "MacBook Pro" {
			t.Fatalf("InsertDevice() = %#v, %v", device, err)
		}
		return tx.InsertCredential(ctx, workspaceOne, identity.CredentialRecord{
			DeviceID: deviceOne, Locator: "AAAAAAAAAAAAAAAAAAAAAA", Scope: "full", Hash: hash, CreatedAt: now,
		})
	})
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	principal, err := store.Authenticate(ctx, workspaceOne, "AAAAAAAAAAAAAAAAAAAAAA", hash, now)
	if err != nil || principal.WorkspaceID != workspaceOne || principal.DeviceID != deviceOne || principal.Scope != "full" {
		t.Fatalf("Authenticate() = %#v, %v", principal, err)
	}
	if _, err := store.Authenticate(ctx, workspaceTwo, "AAAAAAAAAAAAAAAAAAAAAA", hash, now); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("cross-workspace Authenticate() error = %v", err)
	}
	if _, err := store.Authenticate(ctx, workspaceOne, "AAAAAAAAAAAAAAAAAAAAAA", bytes.Repeat([]byte{0x42}, 32), now); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("wrong-secret Authenticate() error = %v", err)
	}
}

func TestAuthenticateRejectsCredentialRevokedBeforeLastUsedUpdate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool := testdb.New(t)
	store := New(pool)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	hash := bytes.Repeat([]byte{0x43}, 32)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		if _, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "Race Mac", Platform: "macos", Role: "full", CreatedAt: now,
		}); err != nil {
			return err
		}
		return tx.InsertCredential(ctx, workspaceOne, identity.CredentialRecord{
			DeviceID: deviceOne, Locator: "BBBBBBBBBBBBBBBBBBBBBB", Scope: "full", Hash: hash, CreatedAt: now,
		})
	})
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin credential lock: %v", err)
	}
	defer func() { _ = lockTx.Rollback(context.Background()) }()
	if _, err := lockTx.Exec(ctx, `
select 1 from credentials
where workspace_id = $1::uuid and token_id = $2
for update`, workspaceOne, "BBBBBBBBBBBBBBBBBBBBBB"); err != nil {
		t.Fatalf("lock credential: %v", err)
	}

	type authenticationResult struct {
		principal identity.Principal
		err       error
	}
	result := make(chan authenticationResult, 1)
	go func() {
		principal, err := store.Authenticate(ctx, workspaceOne, "BBBBBBBBBBBBBBBBBBBBBB", hash, now.Add(time.Minute))
		result <- authenticationResult{principal: principal, err: err}
	}()
	waitForBlockedLastUsedUpdate(t, ctx, pool)

	if _, err := lockTx.Exec(ctx, `
update credentials set revoked_at = $3
where workspace_id = $1::uuid and token_id = $2`, workspaceOne, "BBBBBBBBBBBBBBBBBBBBBB", now); err != nil {
		t.Fatalf("revoke credential: %v", err)
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("commit credential revocation: %v", err)
	}

	got := <-result
	if !errors.Is(got.err, identity.ErrUnauthorized) {
		t.Fatalf("Authenticate() error = %v", got.err)
	}
	if got.principal != (identity.Principal{}) {
		t.Fatalf("Authenticate() principal = %#v", got.principal)
	}
}

func waitForBlockedLastUsedUpdate(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
select exists(
    select 1
    from pg_stat_activity
    where datname = current_database()
      and pid <> pg_backend_pid()
      and wait_event_type = 'Lock'
      and position('update credentials set last_used_at' in query) > 0
)`).Scan(&waiting); err != nil {
			t.Fatalf("inspect blocked authentication update: %v", err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("authentication update did not block on credential row")
		case <-ticker.C:
		}
	}
}

func TestDeviceNameSuffixIsWorkspaceLocalAndCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		if err := tx.InsertWorkspace(ctx, workspaceTwo, now); err != nil {
			return err
		}
		first, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: deviceOne, DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil || first.DisplayName != "MacBook Pro" {
			t.Fatalf("first device = %#v, %v", first, err)
		}
		second, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: "00000000-0000-4000-8000-000000000202", DisplayName: "macbook pro", Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil || second.DisplayName != "macbook pro (2)" {
			t.Fatalf("second device = %#v, %v", second, err)
		}
		other, err := tx.InsertDevice(ctx, workspaceTwo, identity.Device{ID: "00000000-0000-4000-8000-000000000203", DisplayName: "MACBOOK PRO", Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil || other.DisplayName != "MACBOOK PRO" {
			t.Fatalf("other workspace device = %#v, %v", other, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("device suffix transaction: %v", err)
	}
}

func TestDeviceNameSuffixUsesUnicodeCaseFolding(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		first, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "Straße", Platform: "macos", Role: "full", CreatedAt: now,
		})
		if err != nil || first.DisplayName != "Straße" {
			t.Fatalf("first device = %#v, %v", first, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed Unicode device: %v", err)
	}
	if _, err := pool.Exec(ctx, `
update devices set revoked_at = $2
where workspace_id = $1::uuid`, workspaceOne, now); err != nil {
		t.Fatalf("revoke Unicode device: %v", err)
	}

	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		second, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: "00000000-0000-4000-8000-000000000204", DisplayName: "STRASSE", Platform: "macos", Role: "full", CreatedAt: now,
		})
		if err != nil || second.DisplayName != "STRASSE (2)" {
			t.Fatalf("second device = %#v, %v", second, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Unicode case-fold insertion: %v", err)
	}
}

func TestIdempotencyAndRateLimitPersistence(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	keyHash := bytes.Repeat([]byte{0x51}, 32)
	requestHash := bytes.Repeat([]byte{0x52}, 32)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		return tx.PutIdempotency(ctx, identity.IdempotencyRecord{
			ScopeID: "public", Operation: "workspace.create", KeyHash: keyHash, RequestHash: requestHash,
			Response: identity.StoredResponse{Status: 201, ContentType: "application/json", Envelope: secure.Envelope{
				KeyID: "test-key", Nonce: bytes.Repeat([]byte{0x53}, 12), Ciphertext: []byte{0x54},
			}},
		})
	})
	if err != nil {
		t.Fatalf("PutIdempotency() error = %v", err)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		got, err := tx.GetIdempotency(ctx, "public", "workspace.create", keyHash)
		if err != nil || !bytes.Equal(got.RequestHash, requestHash) || got.Response.Status != 201 {
			t.Fatalf("GetIdempotency() metadata mismatch: err=%v status=%d", err, got.Response.Status)
		}
		if got.ScopeID != "public" || got.Expired || got.ExpiresAt.Sub(got.CreatedAt) != identity.IdempotencyLifetime {
			t.Fatalf("idempotency lifetime metadata mismatch: scope=%q expired=%v lifetime=%v", got.ScopeID, got.Expired, got.ExpiresAt.Sub(got.CreatedAt))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("idempotency lookup transaction: %v", err)
	}
	rule := identity.RateRule{Scope: "workspace.create", Limit: 2, Window: time.Minute}
	for call := 1; call <= 3; call++ {
		decision, err := store.ConsumeRateLimit(ctx, rule, bytes.Repeat([]byte{0x61}, 32), now)
		if err != nil {
			t.Fatalf("ConsumeRateLimit() error = %v", err)
		}
		if decision.Allowed != (call <= 2) {
			t.Fatalf("call %d Allowed = %v", call, decision.Allowed)
		}
	}
}

func TestRateLimitFixedWindowResetAndRetention(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	windowStart := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	rule := identity.RateRule{Scope: "pairing.lookup", Limit: 2, Window: 5 * time.Minute}
	subjectHash := bytes.Repeat([]byte{0x62}, 32)

	for call := 1; call <= 2; call++ {
		decision, err := store.ConsumeRateLimit(ctx, rule, subjectHash, windowStart)
		if err != nil {
			t.Fatalf("initial ConsumeRateLimit() call %d error = %v", call, err)
		}
		if !decision.Allowed {
			t.Fatalf("initial call %d was denied", call)
		}
	}

	boundary := windowStart.Add(rule.Window)
	decision, err := store.ConsumeRateLimit(ctx, rule, subjectHash, boundary)
	if err != nil {
		t.Fatalf("boundary ConsumeRateLimit() error = %v", err)
	}
	if !decision.Allowed {
		t.Fatal("first request in reset window was denied")
	}

	var count int
	var storedStart time.Time
	var storedExpires time.Time
	if err := pool.QueryRow(ctx, `
select request_count, window_started_at, expires_at
from rate_limit_buckets
where scope = $1 and subject_hash = $2`, rule.Scope, subjectHash).Scan(
		&count, &storedStart, &storedExpires,
	); err != nil {
		t.Fatalf("inspect reset rate limit: %v", err)
	}
	wantExpires := boundary.Add(rule.Window + identity.RateLimitRetention)
	if count != 1 {
		t.Fatalf("reset request_count = %d", count)
	}
	if !storedStart.Equal(boundary) {
		t.Fatal("reset window_started_at differs from boundary")
	}
	if !storedExpires.Equal(wantExpires) {
		t.Fatal("reset expires_at differs from window end plus retention")
	}
}

func TestRateLimitRejectsNonPositiveWindowWithoutPersistence(t *testing.T) {
	for _, test := range []struct {
		name   string
		window time.Duration
	}{
		{name: "zero", window: 0},
		{name: "negative", window: -time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			pool := testdb.New(t)
			store := New(pool)
			_, err := store.ConsumeRateLimit(ctx, identity.RateRule{
				Scope: "workspace.create", Limit: 2, Window: test.window,
			}, bytes.Repeat([]byte{0x63}, 32), time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC))
			if !errors.Is(err, identity.ErrInvalid) {
				t.Errorf("ConsumeRateLimit() error = %v", err)
			}

			var rows int
			if err := pool.QueryRow(ctx, "select count(*) from rate_limit_buckets").Scan(&rows); err != nil {
				t.Fatalf("count rate-limit rows: %v", err)
			}
			if rows != 0 {
				t.Fatalf("rate-limit rows = %d", rows)
			}
		})
	}
}

func TestIdempotencyScopeIsolation(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	keyHash := bytes.Repeat([]byte{0x71}, 32)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		if err := tx.InsertWorkspace(ctx, workspaceTwo, now); err != nil {
			return err
		}
		for _, item := range []struct {
			scopeID     string
			requestByte byte
		}{
			{scopeID: workspaceOne, requestByte: 0x72},
			{scopeID: workspaceTwo, requestByte: 0x73},
		} {
			if err := tx.PutIdempotency(ctx, identity.IdempotencyRecord{
				ScopeID: item.scopeID, Operation: "device.rename", KeyHash: keyHash,
				WorkspaceID: item.scopeID, RequestHash: bytes.Repeat([]byte{item.requestByte}, 32),
				Response: identity.StoredResponse{Status: 200, ContentType: "application/json", Envelope: secure.Envelope{
					KeyID: "test-key", Nonce: bytes.Repeat([]byte{item.requestByte + 1}, 12), Ciphertext: []byte{item.requestByte + 2},
				}},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed scoped idempotency: %v", err)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		first, err := tx.GetIdempotency(ctx, workspaceOne, "device.rename", keyHash)
		if err != nil {
			return err
		}
		second, err := tx.GetIdempotency(ctx, workspaceTwo, "device.rename", keyHash)
		if err != nil {
			return err
		}
		if bytes.Equal(first.RequestHash, second.RequestHash) {
			t.Fatal("workspace-scoped idempotency records were not independent")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read scoped idempotency: %v", err)
	}
}
