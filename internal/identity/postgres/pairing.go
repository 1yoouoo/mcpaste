package postgres

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/jackc/pgx/v5"
)

func (s *txStore) InsertPairing(ctx context.Context, pairing identity.Pairing) error {
	command, err := s.tx.Exec(ctx, `
insert into pairing_requests(
    id, short_code, claim_hash, proposed_name, platform, requested_scope,
    created_at, expires_at, metadata_purge_at
) values ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9)
on conflict do nothing`,
		pairing.ID, pairing.ShortCode, pairing.ClaimHash, pairing.ProposedName,
		pairing.Platform, pairing.RequestedScope, pairing.CreatedAt,
		pairing.ExpiresAt, pairing.MetadataPurgeAt,
	)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return identity.ErrInvalid
	}
	return nil
}

func (s *txStore) GetPairingByID(ctx context.Context, workspaceID, pairingID string, now time.Time) (identity.Pairing, error) {
	pairing, err := scanPairing(s.tx.QueryRow(ctx, pairingSelect+`
where id = $2::uuid and (workspace_id is null or workspace_id = $1::uuid)`, workspaceID, pairingID))
	if err != nil {
		return identity.Pairing{}, err
	}
	if !pairing.ExpiresAt.After(now) {
		return identity.Pairing{}, identity.ErrPairingExpired
	}
	return pairing, nil
}

func (s *txStore) GetPairingByShortCode(ctx context.Context, workspaceID, shortCode string, now time.Time) (identity.Pairing, error) {
	pairing, err := scanPairing(s.tx.QueryRow(ctx, pairingSelect+`
where short_code = $2 and (workspace_id is null or workspace_id = $1::uuid)`, workspaceID, shortCode))
	if err != nil {
		return identity.Pairing{}, err
	}
	if !pairing.ExpiresAt.After(now) {
		return identity.Pairing{}, identity.ErrPairingExpired
	}
	return pairing, nil
}

func (s *txStore) LockPairingForApproval(ctx context.Context, workspaceID, pairingID string, now time.Time) (identity.Pairing, error) {
	pairing, err := scanPairing(s.tx.QueryRow(ctx, pairingSelect+`
where id = $2::uuid and (workspace_id is null or workspace_id = $1::uuid)
for update`, workspaceID, pairingID))
	if err != nil {
		return identity.Pairing{}, err
	}
	if pairing.WorkspaceID != "" {
		return identity.Pairing{}, identity.ErrPairingApproved
	}
	if !pairing.ExpiresAt.After(now) {
		return identity.Pairing{}, identity.ErrPairingExpired
	}
	return pairing, nil
}

func (s *txStore) ApprovePairing(ctx context.Context, workspaceID, pairingID, approverDeviceID, joiningDeviceID string, approvedAt, claimExpiresAt time.Time, grant secure.Envelope, purgeAt time.Time) error {
	command, err := s.tx.Exec(ctx, `
update pairing_requests set
    workspace_id = $1::uuid,
    approved_by_device_id = $3::uuid,
    device_id = $4::uuid,
    approved_at = $5,
    claim_expires_at = $6,
    grant_key_id = $7,
    grant_nonce = $8,
    grant_ciphertext = $9,
    metadata_purge_at = $10
where id = $2::uuid and workspace_id is null and expires_at > $5`,
		workspaceID, pairingID, approverDeviceID, joiningDeviceID,
		approvedAt, claimExpiresAt, grant.KeyID, grant.Nonce, grant.Ciphertext, purgeAt,
	)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return identity.ErrPairingExpired
	}
	return nil
}

func (s *txStore) LockPairingForClaim(ctx context.Context, pairingID string, claimHash []byte, now time.Time) (identity.Pairing, error) {
	pairing, err := scanPairing(s.tx.QueryRow(ctx, pairingSelect+" where id = $1::uuid for update", pairingID))
	if err != nil {
		return identity.Pairing{}, err
	}
	if subtle.ConstantTimeCompare(pairing.ClaimHash, claimHash) != 1 {
		return identity.Pairing{}, identity.ErrInvalidClaim
	}
	if pairing.WorkspaceID == "" {
		if !pairing.ExpiresAt.After(now) {
			return identity.Pairing{}, identity.ErrPairingExpired
		}
		return identity.Pairing{}, identity.ErrPairingPending
	}
	if !pairing.ClaimInvalidatedAt.IsZero() {
		return identity.Pairing{}, identity.ErrPairingExpired
	}
	if !pairing.ClaimExpiresAt.After(now) {
		return identity.Pairing{}, identity.ErrPairingExpired
	}
	return pairing, nil
}

func (s *txStore) MarkPairingClaimed(ctx context.Context, pairingID string, claimedAt time.Time) error {
	command, err := s.tx.Exec(ctx, `
update pairing_requests set claimed_at = coalesce(claimed_at, $2)
where id = $1::uuid and approved_at is not null and claim_invalidated_at is null`, pairingID, claimedAt)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return identity.ErrPairingExpired
	}
	return nil
}

const pairingSelect = `
select id::text, short_code, claim_hash, proposed_name, platform, requested_scope,
       workspace_id::text, approved_by_device_id::text, device_id::text,
       created_at, expires_at, approved_at, claim_expires_at, claimed_at, claim_invalidated_at,
       grant_key_id, grant_nonce, grant_ciphertext, metadata_purge_at
from pairing_requests `

func scanPairing(row pgx.Row) (identity.Pairing, error) {
	var pairing identity.Pairing
	var workspaceID, approverID, deviceID *string
	var approvedAt, claimExpiresAt, claimedAt, claimInvalidatedAt *time.Time
	var keyID *string
	var nonce, ciphertext []byte
	err := row.Scan(
		&pairing.ID, &pairing.ShortCode, &pairing.ClaimHash,
		&pairing.ProposedName, &pairing.Platform, &pairing.RequestedScope,
		&workspaceID, &approverID, &deviceID,
		&pairing.CreatedAt, &pairing.ExpiresAt, &approvedAt, &claimExpiresAt, &claimedAt, &claimInvalidatedAt,
		&keyID, &nonce, &ciphertext, &pairing.MetadataPurgeAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Pairing{}, identity.ErrNotFound
	}
	if err != nil {
		return identity.Pairing{}, err
	}
	if workspaceID != nil {
		pairing.WorkspaceID = *workspaceID
		pairing.ApprovedByDeviceID = *approverID
		pairing.DeviceID = *deviceID
		pairing.ApprovedAt = *approvedAt
		pairing.ClaimExpiresAt = *claimExpiresAt
		pairing.Grant = secure.Envelope{KeyID: *keyID, Nonce: nonce, Ciphertext: ciphertext}
	}
	if claimedAt != nil {
		pairing.ClaimedAt = *claimedAt
	}
	if claimInvalidatedAt != nil {
		pairing.ClaimInvalidatedAt = *claimInvalidatedAt
	}
	return pairing, nil
}
