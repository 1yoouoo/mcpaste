package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	identitypostgres "github.com/1yoouoo/mcpaste/internal/identity/postgres"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/1yoouoo/mcpaste/internal/testdb"
)

type fixedClock struct{ value time.Time }

func (c fixedClock) Now() time.Time { return c.value }

type countingReader struct{ next byte }

func (r *countingReader) Read(target []byte) (int, error) {
	for index := range target {
		target[index] = r.next
		r.next++
	}
	return len(target), nil
}

type shortCodeGuardStore struct {
	identity.Store
	tx            *shortCodeGuardTx
	rateCalls     int
	withinTxCalls int
}

type shortCodeGuardTx struct {
	identity.TxStore
	lookupCalls int
}

var errMutationTransactionReached = errors.New("mutation transaction reached")

type recoveryPrecomputeStore struct {
	identity.Store
	tx            *recoveryPrecomputeTx
	withinTxCalls atomic.Int32
}

type recoveryPrecomputeTx struct {
	identity.TxStore
}

type recoveryPermitGuardStore struct {
	identity.Store
	rateCalls     atomic.Int32
	withinTxCalls atomic.Int32
}

type observedDoneContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func (s *recoveryPrecomputeStore) ConsumeRateLimit(context.Context, identity.RateRule, []byte, time.Time) (identity.RateDecision, error) {
	return identity.RateDecision{Allowed: true}, nil
}

func (s *recoveryPrecomputeStore) WithinTx(ctx context.Context, fn func(identity.TxStore) error) error {
	if s.withinTxCalls.Add(1) == 1 {
		return fn(s.tx)
	}
	return errMutationTransactionReached
}

func (s *recoveryPrecomputeTx) GetIdempotency(context.Context, string, string, []byte) (identity.IdempotencyRecord, error) {
	return identity.IdempotencyRecord{}, identity.ErrNotFound
}

func (s *recoveryPermitGuardStore) ConsumeRateLimit(context.Context, identity.RateRule, []byte, time.Time) (identity.RateDecision, error) {
	s.rateCalls.Add(1)
	panic("recovery reached rate limiting without a permit")
}

func (s *recoveryPermitGuardStore) WithinTx(context.Context, func(identity.TxStore) error) error {
	s.withinTxCalls.Add(1)
	panic("recovery reached a transaction without a permit")
}

type blockingRecoveryReader struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (r *blockingRecoveryReader) Read(target []byte) (int, error) {
	for index := range target {
		target[index] = byte(index + 1)
	}
	if r.calls.Add(1) == 3 {
		close(r.started)
		<-r.release
	}
	return len(target), nil
}

func (s *shortCodeGuardStore) ConsumeRateLimit(context.Context, identity.RateRule, []byte, time.Time) (identity.RateDecision, error) {
	s.rateCalls++
	panic("malformed short code reached rate limiting")
}

func (s *shortCodeGuardStore) WithinTx(ctx context.Context, fn func(identity.TxStore) error) error {
	s.withinTxCalls++
	return fn(s.tx)
}

func (s *shortCodeGuardTx) GetPairingByShortCode(context.Context, string, string, time.Time) (identity.Pairing, error) {
	s.lookupCalls++
	panic("malformed short code reached repository lookup")
}

func TestCreateWorkspaceReturnsExactlyTwoCredentialsAndReplaysBytes(t *testing.T) {
	pool := testdb.New(t)
	random := &countingReader{next: 1}
	keyring, err := secure.NewKeyring("test-key", map[string][]byte{"test-key": bytes.Repeat([]byte{0x33}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	clock := fixedClock{value: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
	service := identity.NewService(identitypostgres.New(pool), keyring, random, clock)
	idempotencyKey := "00000000-0000-4000-8000-000000000901"
	first, err := service.CreateWorkspace(context.Background(), "192.0.2.10", idempotencyKey, identity.CreateWorkspaceInput{
		DeviceName: "MacBook Pro", Platform: "macos",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	second, err := service.CreateWorkspace(context.Background(), "192.0.2.10", idempotencyKey, identity.CreateWorkspaceInput{
		DeviceName: "MacBook Pro", Platform: "macos",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace() replay error = %v", err)
	}
	if first.Status != 201 || !bytes.Equal(first.Body, second.Body) {
		t.Fatalf("status/replay differ")
	}
	var grant identity.WorkspaceGrant
	if err := json.Unmarshal(first.Body, &grant); err != nil {
		t.Fatalf("unmarshal grant: %v", err)
	}
	if len(grant.Credentials) != 2 {
		t.Fatalf("credential count = %d", len(grant.Credentials))
	}
	if grant.Credentials[0].Kind != "full" || grant.Credentials[1].Kind != "connector" {
		t.Fatal("credential kinds are incorrect")
	}
	if grant.Credentials[0].Token == "" || grant.Credentials[1].Token == "" {
		t.Fatal("one or more issued credentials were empty")
	}
	if grant.RecoveryCode == "" || grant.Device.Role != "full" {
		t.Fatalf("workspace grant metadata is incomplete")
	}
	var storedSecrets int
	if err := pool.QueryRow(context.Background(), `
select count(*) from credentials
where workspace_id = $1::uuid and octet_length(secret_hash) = 32`, grant.WorkspaceID).Scan(&storedSecrets); err != nil {
		t.Fatalf("count credentials: %v", err)
	}
	if storedSecrets != 2 {
		t.Fatalf("stored credential count = %d", storedSecrets)
	}
}

func TestCreateWorkspaceBuildsRecoveryBeforeMutationTransaction(t *testing.T) {
	store := &recoveryPrecomputeStore{tx: &recoveryPrecomputeTx{}}
	random := &blockingRecoveryReader{started: make(chan struct{}), release: make(chan struct{})}
	service := identity.NewService(
		store,
		nil,
		random,
		fixedClock{value: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)},
	)
	result := make(chan error, 1)
	go func() {
		_, err := service.CreateWorkspace(context.Background(), "192.0.2.13", "00000000-0000-4000-8000-000000000904", identity.CreateWorkspaceInput{
			DeviceName: "Precomputed Recovery",
			Platform:   "macos",
		})
		result <- err
	}()
	<-random.started
	if calls := store.withinTxCalls.Load(); calls != 1 {
		t.Fatalf("transactions before recovery generation = %d", calls)
	}
	close(random.release)
	if err := <-result; !errors.Is(err, errMutationTransactionReached) {
		t.Fatal("workspace creation did not reach the mutation transaction after recovery generation")
	}
	if calls := store.withinTxCalls.Load(); calls != 2 {
		t.Fatalf("transactions after recovery generation = %d", calls)
	}
}

func TestThirdRecoveryCancelsBeforeDatabaseWhileTwoPermitsAreHeld(t *testing.T) {
	first, err := secure.AcquireRecoveryPermit(context.Background())
	if err != nil {
		t.Fatal("acquire first recovery permit failed")
	}
	defer first.Release()
	second, err := secure.AcquireRecoveryPermit(context.Background())
	if err != nil {
		t.Fatal("acquire second recovery permit failed")
	}
	defer second.Release()

	store := &recoveryPermitGuardStore{}
	service := identity.NewService(store, nil, nil, fixedClock{value: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)})
	base, cancel := context.WithCancel(context.Background())
	ctx := &observedDoneContext{Context: base, observed: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		_, recoverErr := service.Recover(ctx, "192.0.2.14", "00000000-0000-4000-8000-000000000905", identity.RecoveryInput{
			RecoveryCode: "mcr1.00000000-0000-4000-8000-000000000001." + strings.Repeat("A", 22) + "." + strings.Repeat("A", 43),
			DeviceName:   "Permit Guard",
			Platform:     "macos",
		})
		result <- recoverErr
	}()
	select {
	case <-ctx.observed:
	case <-time.After(time.Second):
		t.Fatal("third recovery did not wait for a permit")
	}
	cancel()
	select {
	case recoverErr := <-result:
		if !errors.Is(recoverErr, context.Canceled) {
			t.Fatal("third recovery did not return context cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("third recovery did not stop after cancellation")
	}
	if store.rateCalls.Load() != 0 || store.withinTxCalls.Load() != 0 {
		t.Fatalf("rate/transaction calls = %d/%d", store.rateCalls.Load(), store.withinTxCalls.Load())
	}
}

func TestCreateWorkspaceRejectsChangedIdempotentRequest(t *testing.T) {
	pool := testdb.New(t)
	random := &countingReader{next: 1}
	keyring, err := secure.NewKeyring("test-key", map[string][]byte{"test-key": bytes.Repeat([]byte{0x44}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	service := identity.NewService(identitypostgres.New(pool), keyring, random, fixedClock{value: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)})
	key := "00000000-0000-4000-8000-000000000902"
	if _, err := service.CreateWorkspace(context.Background(), "192.0.2.11", key, identity.CreateWorkspaceInput{DeviceName: "First", Platform: "macos"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := service.CreateWorkspace(context.Background(), "192.0.2.11", key, identity.CreateWorkspaceInput{DeviceName: "Second", Platform: "macos"}); err != identity.ErrIdempotencyConflict {
		t.Fatalf("changed request error = %v", err)
	}
}

func TestCreateWorkspaceReusesExpiredIdempotencyKey(t *testing.T) {
	pool := testdb.New(t)
	random := &countingReader{next: 1}
	keyring, err := secure.NewKeyring("test-key", map[string][]byte{"test-key": bytes.Repeat([]byte{0x45}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	clock := &fixedClock{value: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
	service := identity.NewService(identitypostgres.New(pool), keyring, random, clock)
	key := "00000000-0000-4000-8000-000000000903"
	input := identity.CreateWorkspaceInput{DeviceName: "Reusable", Platform: "macos"}
	first, err := service.CreateWorkspace(context.Background(), "192.0.2.12", key, input)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
update idempotency_records
set created_at = stamp.now - interval '25 hours',
    expires_at = stamp.now - interval '1 hour'
from (select clock_timestamp() as now) stamp
where scope_id = 'public' and operation = 'workspace.create'`); err != nil {
		t.Fatalf("expire idempotency row: %v", err)
	}
	second, err := service.CreateWorkspace(context.Background(), "192.0.2.12", key, input)
	if err != nil {
		t.Fatalf("expired-key create: %v", err)
	}
	if first.Status != 201 || second.Status != 201 || bytes.Equal(first.Body, second.Body) {
		t.Fatal("expired idempotency key replayed the old workspace")
	}
	var workspaces, idempotencyRows int
	if err := pool.QueryRow(context.Background(), "select count(*) from workspaces").Scan(&workspaces); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if err := pool.QueryRow(context.Background(), `
select count(*) from idempotency_records where scope_id = 'public' and operation = 'workspace.create'`).Scan(&idempotencyRows); err != nil {
		t.Fatalf("count idempotency rows: %v", err)
	}
	if workspaces != 2 || idempotencyRows != 1 {
		t.Fatalf("workspace/idempotency rows = %d/%d", workspaces, idempotencyRows)
	}
}

func TestPairingByShortCodeRejectsMalformedBeforeRateAndTransaction(t *testing.T) {
	store := &shortCodeGuardStore{tx: &shortCodeGuardTx{}}
	service := identity.NewService(
		store, nil, nil,
		fixedClock{value: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)},
	)
	principal := identity.Principal{
		WorkspaceID: "00000000-0000-4000-8000-000000000101",
		DeviceID:    "00000000-0000-4000-8000-000000000201",
		Scope:       "full",
	}
	if _, err := service.PairingByShortCode(context.Background(), principal, "I2345678"); !errors.Is(err, identity.ErrInvalid) {
		t.Fatalf("PairingByShortCode() error = %v", err)
	}
	if store.rateCalls != 0 || store.withinTxCalls != 0 || store.tx.lookupCalls != 0 {
		t.Fatalf("rate/transaction/lookup calls = %d/%d/%d", store.rateCalls, store.withinTxCalls, store.tx.lookupCalls)
	}
}

func TestPairingByShortCodeMalformedLeavesNoRateLimitRowIntegration(t *testing.T) {
	pool := testdb.New(t)
	random := &countingReader{next: 1}
	keyring, err := secure.NewKeyring("test-key", map[string][]byte{"test-key": bytes.Repeat([]byte{0x46}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	service := identity.NewService(
		identitypostgres.New(pool), keyring, random,
		fixedClock{value: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)},
	)
	principal := identity.Principal{
		WorkspaceID: "00000000-0000-4000-8000-000000000101",
		DeviceID:    "00000000-0000-4000-8000-000000000201",
		Scope:       "full",
	}
	if _, err := service.PairingByShortCode(context.Background(), principal, "I2345678"); !errors.Is(err, identity.ErrInvalid) {
		t.Fatalf("PairingByShortCode() error = %v", err)
	}
	var rateRows int
	if err := pool.QueryRow(context.Background(), `
select count(*) from rate_limit_buckets where scope = 'pairing.lookup'`).Scan(&rateRows); err != nil {
		t.Fatalf("count rate-limit rows: %v", err)
	}
	if rateRows != 0 {
		t.Fatalf("malformed short code consumed rate limit: rows = %d", rateRows)
	}
}

func TestClaimDecryptFailureRollsBackAndRemainsCleanupEligible(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	random := &countingReader{next: 1}
	keyring, err := secure.NewKeyring("test-key", map[string][]byte{"test-key": bytes.Repeat([]byte{0x47}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	store := identitypostgres.New(pool)
	service := identity.NewService(store, keyring, random, fixedClock{value: now})
	workspaceID := "00000000-0000-4000-8000-000000000111"
	approverID := "00000000-0000-4000-8000-000000000211"
	joiningID := "00000000-0000-4000-8000-000000000212"
	pairingID := "00000000-0000-4000-8000-000000000311"
	claimSecret, claimHash, err := secure.NewClaimSecret(bytes.NewReader(bytes.Repeat([]byte{0x48}, 32)))
	if err != nil {
		t.Fatalf("NewClaimSecret() error = %v", err)
	}
	credentialHash := bytes.Repeat([]byte{0x49}, 32)
	credentialLocator := "EEEEEEEEEEEEEEEEEEEEEE"
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, now); err != nil {
			return err
		}
		approver, err := tx.InsertDevice(ctx, workspaceID, identity.Device{ID: approverID, DisplayName: "Approver", Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil {
			return err
		}
		if err := tx.InsertPairing(ctx, identity.Pairing{
			ID: pairingID, ShortCode: "2345678C", ClaimHash: claimHash,
			ProposedName: "Joiner", Platform: "linux", RequestedScope: "connector",
			CreatedAt: now, ExpiresAt: now.Add(identity.PairingLifetime),
			MetadataPurgeAt: now.Add(identity.PairingLifetime + identity.PairingMetadataLifetime),
		}); err != nil {
			return err
		}
		joining, err := tx.InsertDevice(ctx, workspaceID, identity.Device{ID: joiningID, DisplayName: "Joiner", Platform: "linux", Role: "connector", CreatedAt: now})
		if err != nil {
			return err
		}
		if err := tx.InsertCredential(ctx, workspaceID, identity.CredentialRecord{
			DeviceID: joining.ID, Locator: credentialLocator, Scope: "connector", Hash: credentialHash, CreatedAt: now,
		}); err != nil {
			return err
		}
		grant := secure.Envelope{KeyID: "missing-key", Nonce: bytes.Repeat([]byte{0x4a}, 12), Ciphertext: []byte{0x4b}}
		return tx.ApprovePairing(ctx, workspaceID, pairingID, approver.ID, joining.ID, now, now.Add(identity.ClaimLifetime), grant, now.Add(identity.ClaimLifetime+identity.PairingMetadataLifetime))
	})
	if err != nil {
		t.Fatalf("seed corrupt claim grant: %v", err)
	}
	if _, err := service.ClaimPairing(ctx, "192.0.2.19", pairingID, claimSecret); err == nil {
		t.Fatal("ClaimPairing() error = nil for missing encryption key")
	}
	var claimedAt *time.Time
	if err := pool.QueryRow(ctx, "select claimed_at from pairing_requests where id = $1::uuid", pairingID).Scan(&claimedAt); err != nil {
		t.Fatalf("inspect claimed_at: %v", err)
	}
	if claimedAt != nil {
		t.Fatal("decrypt failure committed claimed_at")
	}
	result, err := store.Cleanup(ctx, now.Add(6*time.Minute))
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.RevokedDevices != 1 {
		t.Fatalf("RevokedDevices = %d", result.RevokedDevices)
	}
	if _, err := store.Authenticate(ctx, workspaceID, credentialLocator, credentialHash, now.Add(6*time.Minute)); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("cleanup authentication error = %v", err)
	}
	var eventCount int
	if err := pool.QueryRow(ctx, `
select count(*) from workspace_events
where workspace_id = $1::uuid and event_type = 'device.revoked' and object_id = $2::uuid`, workspaceID, joiningID).Scan(&eventCount); err != nil {
		t.Fatalf("count cleanup event: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("cleanup event count = %d", eventCount)
	}
}
