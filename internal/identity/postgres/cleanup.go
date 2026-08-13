package postgres

import (
	"context"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/jackc/pgx/v5"
)

func (s *Store) Cleanup(ctx context.Context, now time.Time) (identity.CleanupResult, error) {
	var result identity.CleanupResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
select workspace_id::text, id::text, device_id::text
from pairing_requests
where approved_at is not null
  and claimed_at is null
  and claim_invalidated_at is null
  and claim_expires_at <= $1
order by claim_expires_at, id
for update skip locked
limit 100`, now)
		if err != nil {
			return err
		}
		type expiredGrant struct {
			workspaceID string
			pairingID   string
			deviceID    string
		}
		expired := make([]expiredGrant, 0, 100)
		for rows.Next() {
			var grant expiredGrant
			if err := rows.Scan(&grant.workspaceID, &grant.pairingID, &grant.deviceID); err != nil {
				rows.Close()
				return err
			}
			expired = append(expired, grant)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		txRepository := &txStore{tx: tx}
		for _, grant := range expired {
			devices, err := tx.Exec(ctx, `
update devices set revoked_at = $3
where workspace_id = $1::uuid and id = $2::uuid and revoked_at is null`,
				grant.workspaceID, grant.deviceID, now)
			if err != nil {
				return err
			}
			result.RevokedDevices += devices.RowsAffected()
			if _, err := tx.Exec(ctx, `
update credentials set revoked_at = $3
where workspace_id = $1::uuid and device_id = $2::uuid and revoked_at is null`,
				grant.workspaceID, grant.deviceID, now); err != nil {
				return err
			}
			if err := txRepository.InsertEvent(ctx, grant.workspaceID, "device.revoked", grant.deviceID, now); err != nil {
				return err
			}
			command, err := tx.Exec(ctx, `
update pairing_requests set claim_invalidated_at = $3
where workspace_id = $1::uuid and id = $2::uuid
  and claimed_at is null and claim_invalidated_at is null`,
				grant.workspaceID, grant.pairingID, now)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return identity.ErrPairingExpired
			}
		}

		pairings, err := tx.Exec(ctx, `
delete from pairing_requests
where metadata_purge_at <= $1
  and (approved_at is null or claimed_at is not null or claim_invalidated_at is not null)`, now)
		if err != nil {
			return err
		}
		result.PairingRows = pairings.RowsAffected()
		idempotency, err := tx.Exec(ctx, "delete from idempotency_records where expires_at <= clock_timestamp()")
		if err != nil {
			return err
		}
		result.IdempotencyRows = idempotency.RowsAffected()
		events, err := tx.Exec(ctx, "delete from workspace_events where expires_at <= $1", now)
		if err != nil {
			return err
		}
		result.EventRows = events.RowsAffected()
		rateLimits, err := tx.Exec(ctx, "delete from rate_limit_buckets where expires_at <= $1", now)
		if err != nil {
			return err
		}
		result.RateLimitRows = rateLimits.RowsAffected()
		return nil
	})
	return result, err
}
