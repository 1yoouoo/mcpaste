package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/1yoouoo/mcpaste/internal/secure"
)

type Service struct {
	store           Store
	keyring         *secure.Keyring
	random          secure.Random
	clock           Clock
	idempotencyGate idempotencyGate
}

type idempotencyGate struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]*idempotencyGateEntry
}

type idempotencyGateEntry struct {
	token      chan struct{}
	references int
}

type CreateWorkspaceInput struct {
	DeviceName string `json:"device_name"`
	Platform   string `json:"platform"`
}

type CreatePairingInput struct {
	ProposedName   string `json:"proposed_name"`
	Platform       string `json:"platform"`
	RequestedScope string `json:"requested_scope"`
}

type RecoveryInput struct {
	RecoveryCode string `json:"recovery_code"`
	DeviceName   string `json:"device_name"`
	Platform     string `json:"platform"`
}

type RenameInput struct {
	DisplayName string `json:"display_name"`
}

const publicIdempotencyScope = "public"

func NewService(store Store, keyring *secure.Keyring, random secure.Random, clock Clock) *Service {
	return &Service{store: store, keyring: keyring, random: random, clock: clock}
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	parsed, err := secure.ParseCredential(token)
	if err != nil {
		return Principal{}, ErrUnauthorized
	}
	return s.store.Authenticate(ctx, parsed.WorkspaceID, parsed.Locator, parsed.Hash, s.clock.Now())
}

func (s *Service) CreateWorkspace(ctx context.Context, clientIP, idempotencyKey string, input CreateWorkspaceInput) (Result, error) {
	name, err := NormalizeDisplayName(input.DeviceName)
	if err != nil || input.Platform != "macos" {
		return Result{}, ErrInvalid
	}
	input.DeviceName = name
	canonical, _ := json.Marshal(input)
	release, err := s.idempotencyGate.acquire(ctx, publicIdempotencyScope, "workspace.create", idempotencyKey)
	if err != nil {
		return Result{}, err
	}
	defer release()
	if replay, found, err := s.preflight(ctx, publicIdempotencyScope, "workspace.create", idempotencyKey, "", canonical); err != nil || found {
		return replay, err
	}
	if err := s.limit(ctx, RateRule{Scope: "workspace.create", Limit: 5, Window: time.Hour}, "ip:"+clientIP); err != nil {
		return Result{}, err
	}
	workspaceID, err := secure.NewUUID(s.random)
	if err != nil {
		return Result{}, err
	}
	deviceID, err := secure.NewUUID(s.random)
	if err != nil {
		return Result{}, err
	}
	recovery, err := secure.NewRecovery(ctx, workspaceID, s.random)
	if err != nil {
		return Result{}, err
	}
	return s.mutate(ctx, publicIdempotencyScope, "workspace.create", idempotencyKey, "", canonical, 201, func(tx TxStore, now time.Time) (any, string, error) {
		if err := tx.InsertWorkspace(ctx, workspaceID, now); err != nil {
			return nil, "", err
		}
		device, err := tx.InsertDevice(ctx, workspaceID, Device{ID: deviceID, DisplayName: name, Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil {
			return nil, "", err
		}
		issued, records, err := s.issueCredentials(workspaceID, deviceID, "full", now)
		if err != nil {
			return nil, "", err
		}
		for _, record := range records {
			if err := tx.InsertCredential(ctx, workspaceID, record); err != nil {
				return nil, "", err
			}
		}
		if err := tx.PutRecovery(ctx, workspaceID, RecoveryRecord{
			WorkspaceID: workspaceID, Locator: recovery.Locator, Verifier: recovery.Verifier,
			CreatedAt: now, RotatedAt: now,
		}); err != nil {
			return nil, "", err
		}
		if err := tx.InsertEvent(ctx, workspaceID, "device.added", deviceID, now); err != nil {
			return nil, "", err
		}
		return WorkspaceGrant{WorkspaceID: workspaceID, Device: grantDevice(device), Credentials: issued, RecoveryCode: recovery.Code}, workspaceID, nil
	})
}

func (s *Service) CreatePairing(ctx context.Context, clientIP, idempotencyKey string, input CreatePairingInput) (Result, error) {
	name, err := NormalizeDisplayName(input.ProposedName)
	if err != nil || (input.Platform != "macos" && input.Platform != "linux") || (input.RequestedScope != "full" && input.RequestedScope != "connector") || input.RequestedScope == "full" && input.Platform != "macos" {
		return Result{}, ErrInvalid
	}
	input.ProposedName = name
	canonical, _ := json.Marshal(input)
	release, err := s.idempotencyGate.acquire(ctx, publicIdempotencyScope, "pairing.create", idempotencyKey)
	if err != nil {
		return Result{}, err
	}
	defer release()
	if replay, found, err := s.preflight(ctx, publicIdempotencyScope, "pairing.create", idempotencyKey, "", canonical); err != nil || found {
		return replay, err
	}
	if err := s.limit(ctx, RateRule{Scope: "pairing.create", Limit: 10, Window: 10 * time.Minute}, "ip:"+clientIP); err != nil {
		return Result{}, err
	}
	return s.mutate(ctx, publicIdempotencyScope, "pairing.create", idempotencyKey, "", canonical, 201, func(tx TxStore, now time.Time) (any, string, error) {
		for attempt := 0; attempt < 8; attempt++ {
			pairingID, err := secure.NewUUID(s.random)
			if err != nil {
				return nil, "", err
			}
			shortCode, err := s.newShortCode()
			if err != nil {
				return nil, "", err
			}
			claimSecret, claimHash, err := secure.NewClaimSecret(s.random)
			if err != nil {
				return nil, "", err
			}
			expiresAt := now.Add(PairingLifetime)
			err = tx.InsertPairing(ctx, Pairing{
				ID: pairingID, ShortCode: shortCode, ClaimHash: claimHash,
				ProposedName: name, Platform: input.Platform, RequestedScope: input.RequestedScope,
				CreatedAt: now, ExpiresAt: expiresAt, MetadataPurgeAt: expiresAt.Add(PairingMetadataLifetime),
			})
			if errors.Is(err, ErrInvalid) {
				continue
			}
			if err != nil {
				return nil, "", err
			}
			return PairingCreateResponse{
				PairingID: pairingID, QRPayload: "mcpaste://pair/" + pairingID,
				ShortCode: shortCode, ClaimSecret: claimSecret, ExpiresAt: wireTime(expiresAt),
			}, "", nil
		}
		return nil, "", errors.New("pairing identifier collision limit reached")
	})
}

func (s *Service) PairingByID(ctx context.Context, principal Principal, pairingID string) (PairingDetails, error) {
	if principal.Scope != "full" {
		return PairingDetails{}, ErrForbidden
	}
	if !secure.ValidUUID(pairingID) {
		return PairingDetails{}, ErrInvalid
	}
	var pairing Pairing
	err := s.store.WithinTx(ctx, func(tx TxStore) error {
		var err error
		pairing, err = tx.GetPairingByID(ctx, principal.WorkspaceID, pairingID, s.clock.Now())
		return err
	})
	return details(pairing), err
}

func (s *Service) PairingByShortCode(ctx context.Context, principal Principal, shortCode string) (PairingDetails, error) {
	if principal.Scope != "full" {
		return PairingDetails{}, ErrForbidden
	}
	if !validShortCode(shortCode) {
		return PairingDetails{}, ErrInvalid
	}
	if err := s.limit(ctx, RateRule{Scope: "pairing.lookup", Limit: 30, Window: 5 * time.Minute}, principal.WorkspaceID+":"+principal.DeviceID); err != nil {
		return PairingDetails{}, err
	}
	var pairing Pairing
	err := s.store.WithinTx(ctx, func(tx TxStore) error {
		var err error
		pairing, err = tx.GetPairingByShortCode(ctx, principal.WorkspaceID, shortCode, s.clock.Now())
		return err
	})
	return details(pairing), err
}

func (s *Service) ApprovePairing(ctx context.Context, principal Principal, pairingID, idempotencyKey string) (Result, error) {
	if principal.Scope != "full" {
		return Result{}, ErrForbidden
	}
	if !secure.ValidUUID(pairingID) {
		return Result{}, ErrInvalid
	}
	canonical := []byte("{}")
	if replay, found, err := s.preflight(ctx, principal.WorkspaceID, "pairing.approve:"+pairingID, idempotencyKey, principal.WorkspaceID, canonical); err != nil || found {
		return replay, err
	}
	return s.mutate(ctx, principal.WorkspaceID, "pairing.approve:"+pairingID, idempotencyKey, principal.WorkspaceID, canonical, 200, func(tx TxStore, now time.Time) (any, string, error) {
		pairing, err := tx.LockPairingForApproval(ctx, principal.WorkspaceID, pairingID, now)
		if err != nil {
			return nil, "", err
		}
		deviceID, err := secure.NewUUID(s.random)
		if err != nil {
			return nil, "", err
		}
		role := pairing.RequestedScope
		device, err := tx.InsertDevice(ctx, principal.WorkspaceID, Device{
			ID: deviceID, DisplayName: pairing.ProposedName, Platform: pairing.Platform, Role: role, CreatedAt: now,
		})
		if err != nil {
			return nil, "", err
		}
		issued, records, err := s.issueCredentials(principal.WorkspaceID, deviceID, role, now)
		if err != nil {
			return nil, "", err
		}
		for _, record := range records {
			if err := tx.InsertCredential(ctx, principal.WorkspaceID, record); err != nil {
				return nil, "", err
			}
		}
		grantBody, err := marshalLine(WorkspaceGrant{WorkspaceID: principal.WorkspaceID, Device: grantDevice(device), Credentials: issued})
		if err != nil {
			return nil, "", err
		}
		grant, err := s.keyring.Encrypt("pairing-grant", pairingID, grantBody)
		if err != nil {
			return nil, "", err
		}
		claimExpiresAt := now.Add(ClaimLifetime)
		if err := tx.ApprovePairing(ctx, principal.WorkspaceID, pairingID, principal.DeviceID, deviceID, now, claimExpiresAt, grant, claimExpiresAt.Add(PairingMetadataLifetime)); err != nil {
			return nil, "", err
		}
		if err := tx.InsertEvent(ctx, principal.WorkspaceID, "device.added", deviceID, now); err != nil {
			return nil, "", err
		}
		return ApprovalResponse{PairingID: pairingID, Status: "approved", ClaimExpiresAt: wireTime(claimExpiresAt)}, principal.WorkspaceID, nil
	})
}

func (s *Service) ClaimPairing(ctx context.Context, clientIP, pairingID, claimSecret string) (Result, error) {
	if !secure.ValidUUID(pairingID) {
		return Result{}, ErrInvalid
	}
	if err := s.limit(ctx, RateRule{Scope: "pairing.claim.ip", Limit: 10, Window: 5 * time.Minute}, "ip:"+clientIP); err != nil {
		return Result{}, err
	}
	if err := s.limit(ctx, RateRule{Scope: "pairing.claim.id", Limit: 10, Window: 5 * time.Minute}, pairingID); err != nil {
		return Result{}, err
	}
	claimHash, err := secure.HashClaimSecret(claimSecret)
	if err != nil {
		return Result{}, ErrInvalidClaim
	}
	var body []byte
	err = s.store.WithinTx(ctx, func(tx TxStore) error {
		now := s.clock.Now()
		pairing, err := tx.LockPairingForClaim(ctx, pairingID, claimHash, now)
		if err != nil {
			return err
		}
		body, err = s.keyring.Decrypt("pairing-grant", pairingID, pairing.Grant)
		if err != nil {
			return err
		}
		return tx.MarkPairingClaimed(ctx, pairingID, now)
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Status: 200, Body: body}, nil
}

func (s *Service) ListDevices(ctx context.Context, principal Principal) ([]DeviceSummary, error) {
	if principal.Scope != "full" {
		return nil, ErrForbidden
	}
	var devices []Device
	err := s.store.WithinTx(ctx, func(tx TxStore) error {
		var err error
		devices, err = tx.ListDevices(ctx, principal.WorkspaceID, principal.DeviceID)
		return err
	})
	if err != nil {
		return nil, err
	}
	summaries := make([]DeviceSummary, 0, len(devices))
	for _, device := range devices {
		summaries = append(summaries, deviceSummary(device))
	}
	return summaries, nil
}

func (s *Service) RenameDevice(ctx context.Context, principal Principal, deviceID, idempotencyKey string, input RenameInput) (Result, error) {
	if principal.Scope != "full" {
		return Result{}, ErrForbidden
	}
	if !secure.ValidUUID(deviceID) {
		return Result{}, ErrInvalid
	}
	name, err := NormalizeDisplayName(input.DisplayName)
	if err != nil {
		return Result{}, err
	}
	input.DisplayName = name
	canonical, _ := json.Marshal(input)
	return s.mutate(ctx, principal.WorkspaceID, "device.rename:"+deviceID, idempotencyKey, principal.WorkspaceID, canonical, 200, func(tx TxStore, now time.Time) (any, string, error) {
		device, err := tx.RenameDevice(ctx, principal.WorkspaceID, deviceID, name, now)
		if err != nil {
			return nil, "", err
		}
		if err := tx.InsertEvent(ctx, principal.WorkspaceID, "device.renamed", deviceID, now); err != nil {
			return nil, "", err
		}
		device.IsCurrent = device.ID == principal.DeviceID
		return struct {
			Device DeviceSummary `json:"device"`
		}{Device: deviceSummary(device)}, principal.WorkspaceID, nil
	})
}

func (s *Service) RevokeDevice(ctx context.Context, principal Principal, deviceID, idempotencyKey string) (Result, error) {
	if principal.Scope != "full" {
		return Result{}, ErrForbidden
	}
	if !secure.ValidUUID(deviceID) {
		return Result{}, ErrInvalid
	}
	if deviceID == principal.DeviceID {
		return Result{}, ErrInvalid
	}
	return s.mutate(ctx, principal.WorkspaceID, "device.revoke:"+deviceID, idempotencyKey, principal.WorkspaceID, []byte("{}"), 204, func(tx TxStore, now time.Time) (any, string, error) {
		if err := tx.RevokeDevice(ctx, principal.WorkspaceID, deviceID, now); err != nil {
			return nil, "", err
		}
		if err := tx.InsertEvent(ctx, principal.WorkspaceID, "device.revoked", deviceID, now); err != nil {
			return nil, "", err
		}
		return nil, principal.WorkspaceID, nil
	})
}

func (s *Service) Recover(ctx context.Context, clientIP, idempotencyKey string, input RecoveryInput) (Result, error) {
	name, err := NormalizeDisplayName(input.DeviceName)
	if err != nil || input.Platform != "macos" {
		return Result{}, ErrInvalid
	}
	workspaceID, locator, err := secure.RecoveryLocator(input.RecoveryCode)
	if err != nil {
		return Result{}, ErrInvalidRecovery
	}
	input.DeviceName = name
	canonical, _ := json.Marshal(input)
	release, err := s.idempotencyGate.acquire(ctx, workspaceID, "recovery", idempotencyKey)
	if err != nil {
		return Result{}, err
	}
	defer release()
	permit, err := secure.AcquireRecoveryPermit(ctx)
	if err != nil {
		return Result{}, err
	}
	defer permit.Release()
	if replay, found, err := s.preflight(ctx, workspaceID, "recovery", idempotencyKey, workspaceID, canonical); err != nil || found {
		return replay, err
	}
	if err := s.limit(ctx, RateRule{Scope: "recovery.ip", Limit: 5, Window: 30 * time.Minute}, "ip:"+clientIP); err != nil {
		return Result{}, err
	}
	if err := s.limit(ctx, RateRule{Scope: "recovery.locator", Limit: 5, Window: 30 * time.Minute}, workspaceID+":"+locator); err != nil {
		return Result{}, err
	}
	rotated, err := secure.NewRecoveryWithPermit(ctx, permit, workspaceID, s.random)
	if err != nil {
		return Result{}, err
	}
	return s.mutate(ctx, workspaceID, "recovery", idempotencyKey, workspaceID, canonical, 201, func(tx TxStore, now time.Time) (any, string, error) {
		stored, err := tx.GetRecovery(ctx, workspaceID, locator)
		if errors.Is(err, ErrInvalidRecovery) {
			return nil, "", ErrInvalidRecovery
		}
		if err != nil {
			return nil, "", err
		}
		if secure.VerifyRecoveryWithPermit(ctx, permit, input.RecoveryCode, workspaceID, locator, stored.Verifier) != nil {
			return nil, "", ErrInvalidRecovery
		}
		deviceID, err := secure.NewUUID(s.random)
		if err != nil {
			return nil, "", err
		}
		device, err := tx.InsertDevice(ctx, workspaceID, Device{ID: deviceID, DisplayName: name, Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil {
			return nil, "", err
		}
		issued, records, err := s.issueCredentials(workspaceID, deviceID, "full", now)
		if err != nil {
			return nil, "", err
		}
		for _, record := range records {
			if err := tx.InsertCredential(ctx, workspaceID, record); err != nil {
				return nil, "", err
			}
		}
		if err := tx.PutRecovery(ctx, workspaceID, RecoveryRecord{WorkspaceID: workspaceID, Locator: rotated.Locator, Verifier: rotated.Verifier, CreatedAt: stored.CreatedAt, RotatedAt: now}); err != nil {
			return nil, "", err
		}
		if err := tx.InsertEvent(ctx, workspaceID, "device.added", deviceID, now); err != nil {
			return nil, "", err
		}
		if err := tx.InsertEvent(ctx, workspaceID, "recovery.rotated", deviceID, now); err != nil {
			return nil, "", err
		}
		return WorkspaceGrant{WorkspaceID: workspaceID, Device: grantDevice(device), Credentials: issued, RecoveryCode: rotated.Code}, workspaceID, nil
	})
}

func (s *Service) Cleanup(ctx context.Context) (CleanupResult, error) {
	result, err := s.store.Cleanup(ctx, s.clock.Now())
	if err != nil {
		return result, err
	}
	result.TextRevisionRows, result.TextPasteRows, err = s.store.PurgeText(ctx, s.clock.Now())
	return result, err
}

func (s *Service) issueCredentials(workspaceID, deviceID, role string, now time.Time) ([]CredentialResponse, []CredentialRecord, error) {
	kinds := []string{"connector"}
	if role == "full" {
		kinds = []string{"full", "connector"}
	}
	responses := make([]CredentialResponse, 0, len(kinds))
	records := make([]CredentialRecord, 0, len(kinds))
	for _, kind := range kinds {
		issued, err := secure.NewCredential(workspaceID, kind, s.random)
		if err != nil {
			return nil, nil, err
		}
		responses = append(responses, CredentialResponse{Kind: kind, Token: issued.Token})
		records = append(records, CredentialRecord{DeviceID: deviceID, Locator: issued.Locator, Scope: kind, Hash: issued.Hash, CreatedAt: now})
	}
	return responses, records, nil
}

func (s *Service) preflight(ctx context.Context, scopeID, operation, key, workspaceID string, canonical []byte) (Result, bool, error) {
	keyHash, requestHash, err := idempotencyHashes(key, canonical)
	if err != nil {
		return Result{}, false, err
	}
	var record IdempotencyRecord
	err = s.store.WithinTx(ctx, func(tx TxStore) error {
		var lookupErr error
		record, lookupErr = tx.GetIdempotency(ctx, scopeID, operation, keyHash)
		return lookupErr
	})
	if errors.Is(err, ErrNotFound) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	if record.Expired {
		return Result{}, false, nil
	}
	result, err := s.decodeIdempotency(record, workspaceID, requestHash)
	return result, true, err
}

func (s *Service) mutate(ctx context.Context, scopeID, operation, key, workspaceID string, canonical []byte, status int, fn func(TxStore, time.Time) (any, string, error)) (Result, error) {
	keyHash, requestHash, err := idempotencyHashes(key, canonical)
	if err != nil {
		return Result{}, err
	}
	var result Result
	err = s.store.WithinTx(ctx, func(tx TxStore) error {
		if err := tx.LockIdempotency(ctx, scopeID, operation, keyHash); err != nil {
			return err
		}
		now := s.clock.Now()
		existing, err := tx.GetIdempotency(ctx, scopeID, operation, keyHash)
		if err == nil && !existing.Expired {
			decoded, err := s.decodeIdempotency(existing, workspaceID, requestHash)
			result = decoded
			return err
		}
		if err == nil {
			if err := tx.DeleteIdempotency(ctx, scopeID, operation, keyHash); err != nil {
				return err
			}
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		response, responseWorkspaceID, err := fn(tx, now)
		if err != nil {
			return err
		}
		var body []byte
		if response != nil {
			body, err = marshalLine(response)
			if err != nil {
				return err
			}
		}
		envelope, err := s.keyring.Encrypt("idempotency", scopeID+":"+operation+":"+hex.EncodeToString(keyHash), body)
		if err != nil {
			return err
		}
		record := IdempotencyRecord{
			ScopeID: scopeID, Operation: operation, KeyHash: keyHash, WorkspaceID: responseWorkspaceID,
			RequestHash: requestHash, Response: StoredResponse{Status: status, ContentType: "application/json", Envelope: envelope},
		}
		if err := tx.PutIdempotency(ctx, record); err != nil {
			return err
		}
		result = Result{Status: status, Body: body}
		return nil
	})
	return result, err
}

func (s *Service) decodeIdempotency(record IdempotencyRecord, workspaceID string, requestHash []byte) (Result, error) {
	if !bytes.Equal(record.RequestHash, requestHash) || workspaceID != "" && record.WorkspaceID != workspaceID {
		return Result{}, ErrIdempotencyConflict
	}
	body, err := s.keyring.Decrypt("idempotency", record.ScopeID+":"+record.Operation+":"+hex.EncodeToString(record.KeyHash), record.Response.Envelope)
	if err != nil {
		return Result{}, err
	}
	return Result{Status: record.Response.Status, Body: body}, nil
}

func (s *Service) limit(ctx context.Context, rule RateRule, subject string) error {
	hash := sha256.Sum256([]byte("mcpaste-rate-v1\x00" + rule.Scope + "\x00" + subject))
	decision, err := s.store.ConsumeRateLimit(ctx, rule, hash[:], s.clock.Now())
	if err != nil {
		return err
	}
	if !decision.Allowed {
		return &RateLimitError{RetryAfter: decision.RetryAfter}
	}
	return nil
}

func (g *idempotencyGate) acquire(ctx context.Context, scopeID, operation, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte("mcpaste-idempotency-gate-v1\x00" + scopeID + "\x00" + operation + "\x00" + key))
	g.mu.Lock()
	if g.entries == nil {
		g.entries = make(map[[sha256.Size]byte]*idempotencyGateEntry)
	}
	entry := g.entries[digest]
	if entry == nil {
		entry = &idempotencyGateEntry{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		g.entries[digest] = entry
	}
	entry.references++
	g.mu.Unlock()

	select {
	case <-ctx.Done():
		g.dropReference(digest, entry)
		return nil, ctx.Err()
	case <-entry.token:
		if err := ctx.Err(); err != nil {
			entry.token <- struct{}{}
			g.dropReference(digest, entry)
			return nil, err
		}
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			entry.token <- struct{}{}
			g.dropReference(digest, entry)
		})
	}, nil
}

func (g *idempotencyGate) dropReference(digest [sha256.Size]byte, entry *idempotencyGateEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	entry.references--
	if entry.references == 0 && g.entries[digest] == entry {
		delete(g.entries, digest)
	}
}

type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return ErrRateLimited.Error() }
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

func idempotencyHashes(key string, canonical []byte) ([]byte, []byte, error) {
	if !secure.ValidUUID(strings.ToLower(key)) || key != strings.ToLower(key) {
		return nil, nil, ErrInvalid
	}
	keyDigest := sha256.Sum256([]byte("mcpaste-idempotency-key-v1\x00" + key))
	requestHasher := sha256.New()
	_, _ = requestHasher.Write([]byte("mcpaste-idempotency-request-v1\x00"))
	_, _ = requestHasher.Write(canonical)
	return keyDigest[:], requestHasher.Sum(nil), nil
}

func marshalLine(value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func details(pairing Pairing) PairingDetails {
	status := "pending"
	var claimExpiresAt *time.Time
	if pairing.WorkspaceID != "" {
		status = "approved"
		value := wireTime(pairing.ClaimExpiresAt)
		claimExpiresAt = &value
	}
	return PairingDetails{
		PairingID: pairing.ID, ProposedName: pairing.ProposedName, Platform: pairing.Platform,
		RequestedScope: pairing.RequestedScope, Status: status, ExpiresAt: wireTime(pairing.ExpiresAt),
		ClaimExpiresAt: claimExpiresAt,
	}
}

const shortCodeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

func validShortCode(value string) bool {
	if len(value) != 8 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if strings.IndexByte(shortCodeAlphabet, value[index]) < 0 {
			return false
		}
	}
	return true
}

func (s *Service) newShortCode() (string, error) {
	const accepted = 248
	var builder strings.Builder
	for builder.Len() < 8 {
		var buffer [1]byte
		if _, err := io.ReadFull(s.random, buffer[:]); err != nil {
			return "", err
		}
		if int(buffer[0]) >= accepted {
			continue
		}
		builder.WriteByte(shortCodeAlphabet[int(buffer[0])%len(shortCodeAlphabet)])
	}
	return builder.String(), nil
}

func RetryAfterSeconds(err error) (int, bool) {
	var rateError *RateLimitError
	if !errors.As(err, &rateError) {
		return 0, false
	}
	return int(math.Ceil(rateError.RetryAfter.Seconds())), true
}
