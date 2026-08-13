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
