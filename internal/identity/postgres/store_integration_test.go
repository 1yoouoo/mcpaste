package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
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

var cleanupTestApplicationCounter atomic.Uint64

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
	var lockBackendPID int
	if err := lockTx.QueryRow(ctx, "select pg_backend_pid()").Scan(&lockBackendPID); err != nil {
		t.Fatalf("read credential lock backend PID: %v", err)
	}
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
	waitForBlockedLastUsedUpdate(t, ctx, pool, lockBackendPID)

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

func waitForBlockedLastUsedUpdate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, lockBackendPID int) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
select exists(
    select 1
    from pg_stat_activity waiting_auth
    where waiting_auth.datname = current_database()
      and waiting_auth.pid <> pg_backend_pid()
      and waiting_auth.wait_event_type = 'Lock'
      and position('update credentials set last_used_at' in waiting_auth.query) > 0
      and $1::integer = any(pg_blocking_pids(waiting_auth.pid))
)`, lockBackendPID).Scan(&waiting); err != nil {
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

func TestDeviceNameSuffixUsesPostgreSQLLower(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		first, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "İ", Platform: "macos", Role: "full", CreatedAt: now,
		})
		if err != nil || first.DisplayName != "İ" {
			t.Fatalf("first device = %#v, %v", first, err)
		}
		second, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: "00000000-0000-4000-8000-000000000205", DisplayName: "i", Platform: "macos", Role: "full", CreatedAt: now,
		})
		if err != nil || second.DisplayName != "i (2)" {
			t.Fatalf("second device = %#v, %v", second, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("PostgreSQL lower insertion: %v", err)
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

func TestPairingClaimReplayReturnsSameEncryptedGrant(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	claimHash := bytes.Repeat([]byte{0x71}, 32)
	grant := secure.Envelope{KeyID: "test-key", Nonce: bytes.Repeat([]byte{0x72}, 12), Ciphertext: []byte{0x73, 0x74}}
	pairingID := "00000000-0000-4000-8000-000000000301"
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		approver, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: deviceOne, DisplayName: "Approver", Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil {
			return err
		}
		if err := tx.InsertPairing(ctx, identity.Pairing{
			ID: pairingID, ShortCode: "23456789", ClaimHash: claimHash,
			ProposedName: "Joiner", Platform: "linux", RequestedScope: "connector",
			CreatedAt: now, ExpiresAt: now.Add(identity.PairingLifetime),
			MetadataPurgeAt: now.Add(identity.PairingLifetime + identity.PairingMetadataLifetime),
		}); err != nil {
			return err
		}
		joining, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: "00000000-0000-4000-8000-000000000302", DisplayName: "Joiner", Platform: "linux", Role: "connector", CreatedAt: now})
		if err != nil {
			return err
		}
		return tx.ApprovePairing(ctx, workspaceOne, pairingID, approver.ID, joining.ID, now, now.Add(identity.ClaimLifetime), grant, now.Add(identity.ClaimLifetime+identity.PairingMetadataLifetime))
	})
	if err != nil {
		t.Fatalf("seed approved pairing: %v", err)
	}
	claim := func(claimHash []byte, claimAt time.Time) (identity.Pairing, error) {
		var pairing identity.Pairing
		err := store.WithinTx(ctx, func(tx identity.TxStore) error {
			var err error
			pairing, err = tx.LockPairingForClaim(ctx, pairingID, claimHash, claimAt)
			if err != nil {
				return err
			}
			return tx.MarkPairingClaimed(ctx, pairingID, claimAt)
		})
		return pairing, err
	}
	first, err := claim(claimHash, now)
	if err != nil {
		t.Fatalf("first claim = %v", err)
	}
	second, err := claim(claimHash, now.Add(time.Second))
	if err != nil {
		t.Fatalf("second claim = %v", err)
	}
	if !bytes.Equal(first.Grant.Ciphertext, second.Grant.Ciphertext) || !bytes.Equal(first.Grant.Nonce, second.Grant.Nonce) {
		t.Fatal("claim replay changed encrypted grant")
	}
	if _, err := claim(bytes.Repeat([]byte{0x75}, 32), now); !errors.Is(err, identity.ErrInvalidClaim) {
		t.Fatalf("wrong claim error = %v", err)
	}
}

func TestApprovedPairingDetailsExpireWhileClaimRemainsValid(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	createdAt := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	approvedAt := createdAt.Add(4 * time.Minute)
	detailsAt := createdAt.Add(identity.PairingLifetime + time.Second)
	pairingID := "00000000-0000-4000-8000-000000000331"
	joiningID := "00000000-0000-4000-8000-000000000332"
	claimHash := bytes.Repeat([]byte{0x76}, 32)
	grant := secure.Envelope{KeyID: "test-key", Nonce: bytes.Repeat([]byte{0x77}, 12), Ciphertext: []byte{0x78}}
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, createdAt); err != nil {
			return err
		}
		approver, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "Approver", Platform: "macos", Role: "full", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertPairing(ctx, identity.Pairing{
			ID: pairingID, ShortCode: "2345678D", ClaimHash: claimHash,
			ProposedName: "Joiner", Platform: "linux", RequestedScope: "connector",
			CreatedAt: createdAt, ExpiresAt: createdAt.Add(identity.PairingLifetime),
			MetadataPurgeAt: createdAt.Add(identity.PairingLifetime + identity.PairingMetadataLifetime),
		}); err != nil {
			return err
		}
		joining, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: joiningID, DisplayName: "Joiner", Platform: "linux", Role: "connector", CreatedAt: approvedAt,
		})
		if err != nil {
			return err
		}
		return tx.ApprovePairing(
			ctx, workspaceOne, pairingID, approver.ID, joining.ID,
			approvedAt, approvedAt.Add(identity.ClaimLifetime), grant,
			approvedAt.Add(identity.ClaimLifetime+identity.PairingMetadataLifetime),
		)
	})
	if err != nil {
		t.Fatalf("seed approved pairing: %v", err)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		if _, err := tx.GetPairingByID(ctx, workspaceOne, pairingID, detailsAt); !errors.Is(err, identity.ErrPairingExpired) {
			t.Fatalf("expired ID details error = %v", err)
		}
		if _, err := tx.GetPairingByShortCode(ctx, workspaceOne, "2345678D", detailsAt); !errors.Is(err, identity.ErrPairingExpired) {
			t.Fatalf("expired short-code details error = %v", err)
		}
		pairing, err := tx.LockPairingForClaim(ctx, pairingID, claimHash, detailsAt)
		if err != nil {
			return err
		}
		if !pairing.ClaimExpiresAt.Equal(approvedAt.Add(identity.ClaimLifetime)) {
			t.Fatal("private claim expiry differs from approval-relative window")
		}
		return tx.MarkPairingClaimed(ctx, pairingID, detailsAt)
	})
	if err != nil {
		t.Fatalf("expired-details/private-claim transaction: %v", err)
	}
}

func TestRenameListRevokeAndCrossWorkspaceRejection(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	hash := bytes.Repeat([]byte{0x81}, 32)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		if err := tx.InsertWorkspace(ctx, workspaceTwo, now); err != nil {
			return err
		}
		if _, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: deviceOne, DisplayName: "MacBook Pro (3)", Platform: "macos", Role: "full", CreatedAt: now}); err != nil {
			return err
		}
		if _, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: "00000000-0000-4000-8000-000000000204", DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now}); err != nil {
			return err
		}
		return tx.InsertCredential(ctx, workspaceOne, identity.CredentialRecord{DeviceID: deviceOne, Locator: "BBBBBBBBBBBBBBBBBBBBBB", Scope: "full", Hash: hash, CreatedAt: now})
	})
	if err != nil {
		t.Fatalf("seed devices: %v", err)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		renamed, err := tx.RenameDevice(ctx, workspaceOne, deviceOne, "MACBOOK PRO", now)
		if err != nil || renamed.DisplayName != "MACBOOK PRO (2)" {
			t.Fatalf("RenameDevice() = %#v, %v", renamed, err)
		}
		devices, err := tx.ListDevices(ctx, workspaceOne, deviceOne)
		if err != nil || len(devices) != 2 || !devices[0].IsCurrent {
			t.Fatalf("ListDevices() = %#v, %v", devices, err)
		}
		if _, err := tx.RenameDevice(ctx, workspaceTwo, deviceOne, "stolen", now); !errors.Is(err, identity.ErrNotFound) {
			t.Fatalf("cross-workspace rename error = %v", err)
		}
		if err := tx.RevokeDevice(ctx, workspaceOne, deviceOne, now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("device administration transaction: %v", err)
	}
	if _, err := store.Authenticate(ctx, workspaceOne, "BBBBBBBBBBBBBBBBBBBBBB", hash, now); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("revoked auth error = %v", err)
	}
}

func TestRenameUsesFourthSuffixWhenSecondAndThirdBelongToOtherDevices(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	targetID := "00000000-0000-4000-8000-000000000205"
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		for _, device := range []identity.Device{
			{ID: deviceOne, DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now},
			{ID: "00000000-0000-4000-8000-000000000202", DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now},
			{ID: "00000000-0000-4000-8000-000000000203", DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now},
			{ID: targetID, DisplayName: "Target", Platform: "macos", Role: "full", CreatedAt: now},
		} {
			if _, err := tx.InsertDevice(ctx, workspaceOne, device); err != nil {
				return err
			}
		}
		renamed, err := tx.RenameDevice(ctx, workspaceOne, targetID, "MACBOOK PRO", now)
		if err != nil {
			return err
		}
		if renamed.DisplayName != "MACBOOK PRO (4)" {
			t.Fatalf("RenameDevice() display name = %q", renamed.DisplayName)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("fourth suffix transaction: %v", err)
	}
}

func TestCleanupPurgesExpiredMetadata(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	_, err := store.ConsumeRateLimit(ctx, identity.RateRule{Scope: "cleanup", Limit: 1, Window: time.Minute}, bytes.Repeat([]byte{0x91}, 32), now.Add(-48*time.Hour))
	if err != nil {
		t.Fatalf("seed rate limit: %v", err)
	}
	result, err := store.Cleanup(ctx, now)
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.RateLimitRows != 1 {
		t.Fatalf("RateLimitRows = %d", result.RateLimitRows)
	}
}

func TestPurgeImagesBoundsFilesystemSelectionAndDatabaseDeletion(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
insert into workspaces(id, created_at)
values ($1::uuid, $2)`, workspaceOne, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("seed image workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into pastes(id, workspace_id, paste_kind, created_at)
select ('00000000-0000-4000-8103-' || lpad(n::text, 12, '0'))::uuid,
       $1::uuid, 'image_bundle', $2::timestamptz - interval '48 hours'
from generate_series(1, 101) as rows(n)`, workspaceOne, now); err != nil {
		t.Fatalf("seed image pastes: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into paste_revisions(
    id, workspace_id, paste_id, server_sequence, revision_kind, created_at, expires_at
)
select ('00000000-0000-4000-8104-' || lpad(n::text, 12, '0'))::uuid,
       $1::uuid,
       ('00000000-0000-4000-8103-' || lpad(n::text, 12, '0'))::uuid,
       n, 'image_bundle', $2::timestamptz - interval '48 hours', $2::timestamptz - interval '24 hours'
from generate_series(1, 101) as rows(n)`, workspaceOne, now); err != nil {
		t.Fatalf("seed image revisions: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into paste_assets(
    workspace_id, paste_id, revision_id, asset_index, mime_type, width, height,
    byte_size, storage_key, image_key_id, image_nonce, created_at, expires_at
)
select $1::uuid,
       ('00000000-0000-4000-8103-' || lpad(n::text, 12, '0'))::uuid,
       ('00000000-0000-4000-8104-' || lpad(n::text, 12, '0'))::uuid,
       0, 'image/png', 1, 1, 1,
       'storage/' || n, 'test-key', decode(repeat('ab', 12), 'hex'),
       $2::timestamptz - interval '48 hours', $2::timestamptz - interval '24 hours'
from generate_series(1, 101) as rows(n)`, workspaceOne, now); err != nil {
		t.Fatalf("seed image assets: %v", err)
	}

	expired, err := store.ListExpiredImages(ctx, now, 100)
	if err != nil {
		t.Fatalf("ListExpiredImages() error = %v", err)
	}
	if len(expired) != 100 {
		t.Fatalf("ListExpiredImages() count = %d, want 100", len(expired))
	}
	revisions, assets, err := store.PurgeImages(ctx, now, expired)
	if err != nil {
		t.Fatalf("PurgeImages() error = %v", err)
	}
	if revisions != 100 || assets != 100 {
		t.Fatalf("PurgeImages() counts = %d/%d, want 100/100", revisions, assets)
	}
	var remainingRevisions, remainingAssets int
	if err := pool.QueryRow(ctx, `
select (select count(*) from paste_revisions where revision_kind = 'image_bundle'),
       (select count(*) from paste_assets)`).Scan(&remainingRevisions, &remainingAssets); err != nil {
		t.Fatalf("inspect remaining images: %v", err)
	}
	if remainingRevisions != 1 || remainingAssets != 1 {
		t.Fatalf("remaining image rows = %d/%d, want 1/1", remainingRevisions, remainingAssets)
	}
}

func TestCleanupBoundsEachMetadataPurge(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)

	if _, err := pool.Exec(ctx, `
insert into workspaces(id, created_at)
values ($1::uuid, $2)`, workspaceOne, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("seed cleanup workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into pairing_requests(
    id, short_code, claim_hash, proposed_name, platform, requested_scope,
    created_at, expires_at, metadata_purge_at
)
select ('00000000-0000-4000-8101-' || lpad(n::text, 12, '0'))::uuid,
       'AAAAAA'
           || substr('23456789ABCDEFGHJKMNPQRSTUVWXYZ', ((n - 1) / 31) + 1, 1)
           || substr('23456789ABCDEFGHJKMNPQRSTUVWXYZ', ((n - 1) % 31) + 1, 1),
       decode(repeat('a1', 32), 'hex'), 'Pending ' || n, 'linux', 'connector',
       $1::timestamptz - interval '2 hours',
       $1::timestamptz - interval '1 hour',
       $1::timestamptz - interval '1 hour'
from generate_series(1, 101) as rows(n)`, now); err != nil {
		t.Fatalf("seed expired pairings: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into idempotency_records(
    scope_id, operation, key_hash, workspace_id, request_hash,
    response_status, response_content_type, response_key_id, response_nonce,
    response_ciphertext, created_at, expires_at
)
select 'public', 'cleanup.test', decode(lpad(to_hex(n), 64, '0'), 'hex'), null,
       decode(repeat('b1', 32), 'hex'), 200, 'application/json', 'test-key',
       decode(repeat('b2', 12), 'hex'), decode('b3', 'hex'),
       $1::timestamptz - interval '25 hours', $1::timestamptz - interval '1 hour'
from generate_series(1, 101) as rows(n)`, now); err != nil {
		t.Fatalf("seed expired idempotency records: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into workspace_events(
    workspace_id, sequence, event_type, object_id, created_at, expires_at
)
select $1::uuid, n, 'device.added',
       ('00000000-0000-4000-8102-' || lpad(n::text, 12, '0'))::uuid,
       $2::timestamptz - interval '2 hours', $2::timestamptz - interval '1 hour'
from generate_series(1, 101) as rows(n)`, workspaceOne, now); err != nil {
		t.Fatalf("seed expired workspace events: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into rate_limit_buckets(
    scope, subject_hash, window_started_at, request_count, expires_at
)
select 'cleanup.test', decode(lpad(to_hex(n), 64, '0'), 'hex'),
       $1::timestamptz - interval '2 hours', 1, $1::timestamptz - interval '1 hour'
from generate_series(1, 101) as rows(n)`, now); err != nil {
		t.Fatalf("seed expired rate limit buckets: %v", err)
	}

	want := []int64{100, 1, 0}
	for call, expected := range want {
		result, err := store.Cleanup(ctx, now)
		if err != nil {
			t.Fatalf("Cleanup() call %d error = %v", call+1, err)
		}
		if result.PairingRows != expected || result.IdempotencyRows != expected ||
			result.EventRows != expected || result.RateLimitRows != expected {
			t.Fatalf(
				"Cleanup() call %d rows = pairing:%d idempotency:%d events:%d rate_limits:%d, want %d each",
				call+1, result.PairingRows, result.IdempotencyRows, result.EventRows, result.RateLimitRows, expected,
			)
		}
	}
	var retentionFloor int64
	if err := pool.QueryRow(ctx, `select event_retention_floor from workspaces where id = $1::uuid`, workspaceOne).Scan(&retentionFloor); err != nil {
		t.Fatalf("inspect event retention floor: %v", err)
	}
	if retentionFloor != 101 {
		t.Fatalf("event retention floor = %d, want 101", retentionFloor)
	}
}

func TestClaimAndCleanupSerializeGrantValidity(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	createdAt := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	claimAt := createdAt.Add(4*time.Minute + 59*time.Second)
	cleanupAt := createdAt.Add(6 * time.Minute)
	pairingID := "00000000-0000-4000-8000-000000000311"
	joiningDeviceID := "00000000-0000-4000-8000-000000000312"
	claimHash := bytes.Repeat([]byte{0xa1}, 32)
	credentialHash := bytes.Repeat([]byte{0xa2}, 32)
	credentialLocator := "CCCCCCCCCCCCCCCCCCCCCC"
	grant := secure.Envelope{KeyID: "test-key", Nonce: bytes.Repeat([]byte{0xa3}, 12), Ciphertext: []byte{0xa4}}
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, createdAt); err != nil {
			return err
		}
		approver, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "Approver", Platform: "macos", Role: "full", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertPairing(ctx, identity.Pairing{
			ID: pairingID, ShortCode: "2345678A", ClaimHash: claimHash,
			ProposedName: "Joiner", Platform: "linux", RequestedScope: "connector",
			CreatedAt: createdAt, ExpiresAt: createdAt.Add(identity.PairingLifetime),
			MetadataPurgeAt: createdAt.Add(identity.PairingLifetime + identity.PairingMetadataLifetime),
		}); err != nil {
			return err
		}
		joining, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: joiningDeviceID, DisplayName: "Joiner", Platform: "linux", Role: "connector", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertCredential(ctx, workspaceOne, identity.CredentialRecord{
			DeviceID: joining.ID, Locator: credentialLocator, Scope: "connector", Hash: credentialHash, CreatedAt: createdAt,
		}); err != nil {
			return err
		}
		return tx.ApprovePairing(
			ctx, workspaceOne, pairingID, approver.ID, joining.ID,
			createdAt, createdAt.Add(identity.ClaimLifetime), grant,
			createdAt.Add(identity.ClaimLifetime+identity.PairingMetadataLifetime),
		)
	})
	if err != nil {
		t.Fatalf("seed claim/cleanup race: %v", err)
	}

	type cleanupOutcome struct {
		result identity.CleanupResult
		err    error
	}
	start := make(chan struct{})
	claimDone := make(chan error, 1)
	cleanupDone := make(chan cleanupOutcome, 1)
	go func() {
		<-start
		err := store.WithinTx(ctx, func(tx identity.TxStore) error {
			if _, err := tx.LockPairingForClaim(ctx, pairingID, claimHash, claimAt); err != nil {
				return err
			}
			return tx.MarkPairingClaimed(ctx, pairingID, claimAt)
		})
		claimDone <- err
	}()
	go func() {
		<-start
		result, err := store.Cleanup(ctx, cleanupAt)
		cleanupDone <- cleanupOutcome{result: result, err: err}
	}()
	close(start)
	claimErr := <-claimDone
	cleanup := <-cleanupDone
	if cleanup.err != nil {
		t.Fatalf("Cleanup() error = %v", cleanup.err)
	}

	var claimedAt, invalidatedAt *time.Time
	if err := pool.QueryRow(ctx, `
select claimed_at, claim_invalidated_at
from pairing_requests
where workspace_id = $1::uuid and id = $2::uuid`, workspaceOne, pairingID).Scan(&claimedAt, &invalidatedAt); err != nil {
		t.Fatalf("inspect pairing terminal state: %v", err)
	}
	switch {
	case claimErr == nil:
		if cleanup.result.RevokedDevices != 0 || claimedAt == nil || invalidatedAt != nil {
			t.Fatalf("claim-won state: revoked=%d claimed=%v invalidated=%v", cleanup.result.RevokedDevices, claimedAt != nil, invalidatedAt != nil)
		}
		if _, err := store.Authenticate(ctx, workspaceOne, credentialLocator, credentialHash, cleanupAt); err != nil {
			t.Fatalf("claim-won credential authentication: %v", err)
		}
	case errors.Is(claimErr, identity.ErrPairingExpired):
		if cleanup.result.RevokedDevices != 1 || claimedAt != nil || invalidatedAt == nil {
			t.Fatalf("cleanup-won state: revoked=%d claimed=%v invalidated=%v", cleanup.result.RevokedDevices, claimedAt != nil, invalidatedAt != nil)
		}
		if _, err := store.Authenticate(ctx, workspaceOne, credentialLocator, credentialHash, cleanupAt); !errors.Is(err, identity.ErrUnauthorized) {
			t.Fatalf("cleanup-won authentication error = %v", err)
		}
		var eventCount int
		if err := pool.QueryRow(ctx, `
select count(*)
from workspace_events
where workspace_id = $1::uuid and event_type = 'device.revoked' and object_id = $2::uuid`,
			workspaceOne, joiningDeviceID).Scan(&eventCount); err != nil {
			t.Fatalf("count cleanup event: %v", err)
		}
		if eventCount != 1 {
			t.Fatalf("cleanup device.revoked events = %d", eventCount)
		}
	default:
		t.Fatalf("claim error = %v", claimErr)
	}
}

func TestCleanupWinsDeterministicallyAndRevokesGrant(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	createdAt := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	cleanupAt := createdAt.Add(6 * time.Minute)
	pairingID := "00000000-0000-4000-8000-000000000321"
	joiningDeviceID := "00000000-0000-4000-8000-000000000322"
	claimHash := bytes.Repeat([]byte{0xb1}, 32)
	credentialHash := bytes.Repeat([]byte{0xb2}, 32)
	credentialLocator := "DDDDDDDDDDDDDDDDDDDDDD"
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, createdAt); err != nil {
			return err
		}
		approver, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "Approver", Platform: "macos", Role: "full", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertPairing(ctx, identity.Pairing{
			ID: pairingID, ShortCode: "2345678B", ClaimHash: claimHash,
			ProposedName: "Joiner", Platform: "linux", RequestedScope: "connector",
			CreatedAt: createdAt, ExpiresAt: createdAt.Add(identity.PairingLifetime),
			MetadataPurgeAt: createdAt.Add(identity.PairingLifetime + identity.PairingMetadataLifetime),
		}); err != nil {
			return err
		}
		joining, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: joiningDeviceID, DisplayName: "Joiner", Platform: "linux", Role: "connector", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertCredential(ctx, workspaceOne, identity.CredentialRecord{
			DeviceID: joining.ID, Locator: credentialLocator, Scope: "connector", Hash: credentialHash, CreatedAt: createdAt,
		}); err != nil {
			return err
		}
		grant := secure.Envelope{KeyID: "test-key", Nonce: bytes.Repeat([]byte{0xb3}, 12), Ciphertext: []byte{0xb4}}
		return tx.ApprovePairing(
			ctx, workspaceOne, pairingID, approver.ID, joining.ID,
			createdAt, createdAt.Add(identity.ClaimLifetime), grant,
			createdAt.Add(identity.ClaimLifetime+identity.PairingMetadataLifetime),
		)
	})
	if err != nil {
		t.Fatalf("seed deterministic cleanup: %v", err)
	}
	result, err := store.Cleanup(ctx, cleanupAt)
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.RevokedDevices != 1 {
		t.Fatalf("RevokedDevices = %d", result.RevokedDevices)
	}
	if _, err := store.Authenticate(ctx, workspaceOne, credentialLocator, credentialHash, cleanupAt); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("revoked credential authentication error = %v", err)
	}
	var deviceRevoked, credentialRevoked, invalidatedAt *time.Time
	var eventCount int
	if err := pool.QueryRow(ctx, `
select d.revoked_at, c.revoked_at, p.claim_invalidated_at,
       (select count(*) from workspace_events e
        where e.workspace_id = p.workspace_id and e.event_type = 'device.revoked' and e.object_id = p.device_id)
from pairing_requests p
join devices d on d.workspace_id = p.workspace_id and d.id = p.device_id
join credentials c on c.workspace_id = d.workspace_id and c.device_id = d.id
where p.workspace_id = $1::uuid and p.id = $2::uuid`, workspaceOne, pairingID).Scan(
		&deviceRevoked, &credentialRevoked, &invalidatedAt, &eventCount,
	); err != nil {
		t.Fatalf("inspect cleanup state: %v", err)
	}
	if deviceRevoked == nil || credentialRevoked == nil || invalidatedAt == nil || eventCount != 1 {
		t.Fatalf("cleanup state metadata: device=%v credential=%v invalidated=%v events=%d", deviceRevoked != nil, credentialRevoked != nil, invalidatedAt != nil, eventCount)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		_, err := tx.LockPairingForClaim(ctx, pairingID, claimHash, cleanupAt)
		return err
	})
	if !errors.Is(err, identity.ErrPairingExpired) {
		t.Fatalf("claim after cleanup error = %v", err)
	}
}

func TestCleanupDoesNotDuplicateRevokedDeviceEvent(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	createdAt := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	cleanupAt := createdAt.Add(6 * time.Minute)
	pairingID := "00000000-0000-4000-8000-000000000331"
	joiningDeviceID := "00000000-0000-4000-8000-000000000332"
	claimHash := bytes.Repeat([]byte{0xc1}, 32)
	credentialHash := bytes.Repeat([]byte{0xc2}, 32)
	credentialLocator := "EEEEEEEEEEEEEEEEEEEEEE"

	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, createdAt); err != nil {
			return err
		}
		approver, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "Approver", Platform: "macos", Role: "full", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertPairing(ctx, identity.Pairing{
			ID: pairingID, ShortCode: "2345678C", ClaimHash: claimHash,
			ProposedName: "Joiner", Platform: "linux", RequestedScope: "connector",
			CreatedAt: createdAt, ExpiresAt: createdAt.Add(identity.PairingLifetime),
			MetadataPurgeAt: createdAt.Add(identity.PairingLifetime + identity.PairingMetadataLifetime),
		}); err != nil {
			return err
		}
		joining, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: joiningDeviceID, DisplayName: "Joiner", Platform: "linux", Role: "connector", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertCredential(ctx, workspaceOne, identity.CredentialRecord{
			DeviceID: joining.ID, Locator: credentialLocator, Scope: "connector", Hash: credentialHash, CreatedAt: createdAt,
		}); err != nil {
			return err
		}
		grant := secure.Envelope{KeyID: "test-key", Nonce: bytes.Repeat([]byte{0xc3}, 12), Ciphertext: []byte{0xc4}}
		if err := tx.ApprovePairing(
			ctx, workspaceOne, pairingID, approver.ID, joining.ID,
			createdAt, createdAt.Add(identity.ClaimLifetime), grant,
			createdAt.Add(identity.ClaimLifetime+identity.PairingMetadataLifetime),
		); err != nil {
			return err
		}
		if err := tx.RevokeDevice(ctx, workspaceOne, joining.ID, createdAt.Add(time.Minute)); err != nil {
			return err
		}
		return tx.InsertEvent(ctx, workspaceOne, "device.revoked", joining.ID, createdAt.Add(time.Minute))
	})
	if err != nil {
		t.Fatalf("seed already-revoked grant: %v", err)
	}

	result, err := store.Cleanup(ctx, cleanupAt)
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.RevokedDevices != 0 {
		t.Fatalf("RevokedDevices = %d", result.RevokedDevices)
	}

	var deviceRevoked, credentialRevoked, invalidatedAt *time.Time
	var eventCount int
	if err := pool.QueryRow(ctx, `
select d.revoked_at, c.revoked_at, p.claim_invalidated_at,
       (select count(*) from workspace_events e
        where e.workspace_id = p.workspace_id and e.event_type = 'device.revoked' and e.object_id = p.device_id)
from pairing_requests p
join devices d on d.workspace_id = p.workspace_id and d.id = p.device_id
join credentials c on c.workspace_id = d.workspace_id and c.device_id = d.id
where p.workspace_id = $1::uuid and p.id = $2::uuid`, workspaceOne, pairingID).Scan(
		&deviceRevoked, &credentialRevoked, &invalidatedAt, &eventCount,
	); err != nil {
		t.Fatalf("inspect already-revoked cleanup state: %v", err)
	}
	if deviceRevoked == nil || credentialRevoked == nil || invalidatedAt == nil {
		t.Fatalf(
			"already-revoked cleanup state: device=%v credential=%v invalidated=%v",
			deviceRevoked != nil, credentialRevoked != nil, invalidatedAt != nil,
		)
	}
	if eventCount != 1 {
		t.Fatalf("device.revoked event count = %d", eventCount)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		_, err := tx.LockPairingForClaim(ctx, pairingID, claimHash, cleanupAt)
		return err
	})
	if !errors.Is(err, identity.ErrPairingExpired) {
		t.Fatalf("claim after cleanup error = %v", err)
	}
}

func TestCleanupPrioritizesOldestExpiredGrantAcrossWorkspaces(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)

	seedExpiredCleanupGrants(t, ctx, pool, now, workspaceOne, deviceOne, "8201", "8301", 0, 101, 0, 1)
	seedExpiredCleanupGrants(
		t, ctx, pool, now, workspaceTwo, "00000000-0000-4000-8000-000000000202",
		"8202", "8302", 200, 1, 199, 1,
	)

	result, err := store.Cleanup(ctx, now)
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.RevokedDevices != 100 {
		t.Fatalf("RevokedDevices = %d", result.RevokedDevices)
	}
	var highWorkspaceInvalidated, highWorkspaceRevoked int
	if err := pool.QueryRow(ctx, `
select (select count(*) from pairing_requests
        where workspace_id = $1::uuid and claim_invalidated_at is not null),
       (select count(*) from devices
        where workspace_id = $1::uuid and role = 'connector' and revoked_at is not null)`, workspaceTwo).Scan(
		&highWorkspaceInvalidated, &highWorkspaceRevoked,
	); err != nil {
		t.Fatalf("inspect oldest high-workspace grant: %v", err)
	}
	if highWorkspaceInvalidated != 1 || highWorkspaceRevoked != 1 {
		t.Fatalf(
			"oldest high-workspace grant state = invalidated:%d revoked:%d",
			highWorkspaceInvalidated, highWorkspaceRevoked,
		)
	}
}

func TestConcurrentCleanupAcrossWorkspacesReachesConsistentTerminalState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	basePool := testdb.New(t)
	pool, applicationName := newCleanupTestPool(t, ctx, basePool)
	store := New(pool)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)

	seedExpiredCleanupGrants(t, ctx, pool, now, workspaceOne, deviceOne, "8401", "8501", 0, 60, 0, 2)
	seedExpiredCleanupGrants(
		t, ctx, pool, now, workspaceTwo, "00000000-0000-4000-8000-000000000202",
		"8402", "8502", 100, 60, -1, 2,
	)

	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin cleanup advisory lock blocker: %v", err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err := blocker.Exec(ctx, `
select pg_advisory_xact_lock(hashtextextended($1, 0))`, cleanupAdvisoryLockName); err != nil {
		t.Fatalf("acquire cleanup advisory lock blocker: %v", err)
	}
	var blockerBackendPID int
	if err := blocker.QueryRow(ctx, "select pg_backend_pid()").Scan(&blockerBackendPID); err != nil {
		t.Fatalf("read cleanup blocker backend PID: %v", err)
	}

	type cleanupOutcome struct {
		result identity.CleanupResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan cleanupOutcome, 2)
	for worker := 0; worker < 2; worker++ {
		go func() {
			<-start
			result, err := store.Cleanup(ctx, now)
			outcomes <- cleanupOutcome{result: result, err: err}
		}()
	}
	close(start)
	waitCtx, cancelWait := context.WithTimeout(ctx, 2*time.Second)
	defer cancelWait()
	waitForCleanupWorkersBlocked(t, waitCtx, pool, applicationName, blockerBackendPID)

	var invalidatedWhileBlocked int
	if err := pool.QueryRow(ctx, "select count(*) from pairing_requests where claim_invalidated_at is not null").Scan(
		&invalidatedWhileBlocked,
	); err != nil {
		t.Fatalf("inspect blocked cleanup state: %v", err)
	}
	if invalidatedWhileBlocked != 0 {
		t.Fatalf("invalidated grants while cleanup lock blocked = %d", invalidatedWhileBlocked)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("release cleanup advisory lock blocker: %v", err)
	}

	totalRevoked := int64(0)
	for worker := 0; worker < 2; worker++ {
		outcome := <-outcomes
		if outcome.err != nil {
			t.Fatalf("concurrent Cleanup() error = %v", outcome.err)
		}
		totalRevoked += outcome.result.RevokedDevices
	}
	if totalRevoked != 120 {
		t.Fatalf("concurrent RevokedDevices total = %d", totalRevoked)
	}

	var invalidated, revokedDevices, revokedCredentials, revokedEvents int
	if err := pool.QueryRow(ctx, `
select (select count(*) from pairing_requests where claim_invalidated_at is not null),
       (select count(*) from devices where role = 'connector' and revoked_at is not null),
       (select count(*) from credentials where revoked_at is not null),
       (select count(*) from workspace_events where event_type = 'device.revoked')`).Scan(
		&invalidated, &revokedDevices, &revokedCredentials, &revokedEvents,
	); err != nil {
		t.Fatalf("inspect concurrent cleanup terminal state: %v", err)
	}
	if invalidated != 120 || revokedDevices != 120 || revokedCredentials != 120 || revokedEvents != 120 {
		t.Fatalf(
			"terminal counts = invalidated:%d devices:%d credentials:%d events:%d",
			invalidated, revokedDevices, revokedCredentials, revokedEvents,
		)
	}
	result, err := store.Cleanup(ctx, now)
	if err != nil {
		t.Fatalf("terminal Cleanup() error = %v", err)
	}
	if result.RevokedDevices != 0 {
		t.Fatalf("terminal RevokedDevices = %d", result.RevokedDevices)
	}
}

func newCleanupTestPool(
	t *testing.T,
	ctx context.Context,
	basePool *pgxpool.Pool,
) (*pgxpool.Pool, string) {
	t.Helper()
	applicationName := fmt.Sprintf(
		"mcpaste-cleanup-test-%d-%d",
		os.Getpid(), cleanupTestApplicationCounter.Add(1),
	)
	config := basePool.Config()
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	config.ConnConfig.RuntimeParams["application_name"] = applicationName
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open dedicated cleanup test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("connect dedicated cleanup test pool: %v", err)
	}
	return pool, applicationName
}

func waitForCleanupWorkersBlocked(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	applicationName string,
	blockerBackendPID int,
) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waitingWorkers int
		if err := pool.QueryRow(ctx, `
select count(*)
from pg_stat_activity waiting_cleanup
where waiting_cleanup.datname = current_database()
  and waiting_cleanup.application_name = $2
  and waiting_cleanup.wait_event_type = 'Lock'
  and position('pg_advisory_xact_lock(hashtextextended' in waiting_cleanup.query) > 0
  and $1::integer = any(pg_blocking_pids(waiting_cleanup.pid))`, blockerBackendPID, applicationName).Scan(&waitingWorkers); err != nil {
			if ctx.Err() != nil {
				t.Fatalf("cleanup workers waiting on blocker = %d, want 2", waitingWorkers)
			}
			t.Fatalf("inspect blocked cleanup workers: %v", err)
		}
		if waitingWorkers == 2 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("cleanup workers waiting on blocker = %d, want 2", waitingWorkers)
		case <-ticker.C:
		}
	}
}

func seedExpiredCleanupGrants(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	now time.Time,
	workspaceID string,
	approverDeviceID string,
	deviceUUIDGroup string,
	pairingUUIDGroup string,
	ordinalBase int,
	count int,
	expiryOffset int,
	expiryStride int,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
insert into workspaces(id, created_at)
values ($1::uuid, $2)`, workspaceID, now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("seed cleanup workspace %s: %v", workspaceID, err)
	}
	if _, err := pool.Exec(ctx, `
insert into devices(id, workspace_id, display_name, platform, role, created_at)
values ($1::uuid, $2::uuid, 'Approver', 'macos', 'full', $3)`,
		approverDeviceID, workspaceID, now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("seed cleanup approver for workspace %s: %v", workspaceID, err)
	}
	if _, err := pool.Exec(ctx, `
insert into devices(id, workspace_id, display_name, platform, role, created_at)
select ('00000000-0000-4000-' || $3 || '-' || lpad(($4 + n)::text, 12, '0'))::uuid,
       $1::uuid, 'Joiner ' || n, 'linux', 'connector', $2
from generate_series(1, $5) as rows(n)`,
		workspaceID, now.Add(-10*time.Minute), deviceUUIDGroup, ordinalBase, count); err != nil {
		t.Fatalf("seed cleanup joining devices for workspace %s: %v", workspaceID, err)
	}
	if _, err := pool.Exec(ctx, `
insert into credentials(workspace_id, device_id, token_id, scope, secret_hash, created_at)
select $1::uuid,
       ('00000000-0000-4000-' || $3 || '-' || lpad(($4 + n)::text, 12, '0'))::uuid,
       lpad(($4 + n)::text, 22, 'A'), 'connector', decode(repeat('d1', 32), 'hex'), $2
from generate_series(1, $5) as rows(n)`,
		workspaceID, now.Add(-10*time.Minute), deviceUUIDGroup, ordinalBase, count); err != nil {
		t.Fatalf("seed cleanup credentials for workspace %s: %v", workspaceID, err)
	}
	if _, err := pool.Exec(ctx, `
insert into pairing_requests(
    id, short_code, claim_hash, proposed_name, platform, requested_scope,
    workspace_id, approved_by_device_id, device_id,
    created_at, expires_at, approved_at, claim_expires_at,
    grant_key_id, grant_nonce, grant_ciphertext, metadata_purge_at
)
select ('00000000-0000-4000-' || $4 || '-' || lpad(($5 + n)::text, 12, '0'))::uuid,
       'AAAAA'
           || substr('23456789ABCDEFGHJKMNPQRSTUVWXYZ', ((($5 + n - 1) / 961) % 31) + 1, 1)
           || substr('23456789ABCDEFGHJKMNPQRSTUVWXYZ', ((($5 + n - 1) / 31) % 31) + 1, 1)
           || substr('23456789ABCDEFGHJKMNPQRSTUVWXYZ', (($5 + n - 1) % 31) + 1, 1),
       decode(repeat('d2', 32), 'hex'), 'Joiner ' || n, 'linux', 'connector',
       $1::uuid, $2::uuid,
       ('00000000-0000-4000-' || $3 || '-' || lpad(($5 + n)::text, 12, '0'))::uuid,
       $6::timestamptz - interval '10 minutes', $6::timestamptz + interval '1 hour',
       $6::timestamptz - interval '9 minutes',
       $6::timestamptz - (($7 + $8 * n) * interval '1 second'),
       'test-key', decode(repeat('d3', 12), 'hex'), decode('d4', 'hex'),
       $6::timestamptz + interval '24 hours'
from generate_series(1, $9) as rows(n)`,
		workspaceID, approverDeviceID, deviceUUIDGroup, pairingUUIDGroup, ordinalBase,
		now, expiryOffset, expiryStride, count,
	); err != nil {
		t.Fatalf("seed expired cleanup grants for workspace %s: %v", workspaceID, err)
	}
}
