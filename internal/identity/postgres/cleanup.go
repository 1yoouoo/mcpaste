package postgres

import (
	"context"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/jackc/pgx/v5"
)

const cleanupAdvisoryLockName = "mcpaste.identity.cleanup"

func (s *Store) Cleanup(ctx context.Context, now time.Time) (identity.CleanupResult, error) {
	var result identity.CleanupResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
select pg_advisory_xact_lock(hashtextextended($1, 0))`, cleanupAdvisoryLockName); err != nil {
			return err
		}
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
			revokedDevices := devices.RowsAffected()
			result.RevokedDevices += revokedDevices
			if _, err := tx.Exec(ctx, `
update credentials set revoked_at = $3
where workspace_id = $1::uuid and device_id = $2::uuid and revoked_at is null`,
				grant.workspaceID, grant.deviceID, now); err != nil {
				return err
			}
			if revokedDevices == 1 {
				if err := txRepository.InsertEvent(ctx, grant.workspaceID, "device.revoked", grant.deviceID, now); err != nil {
					return err
				}
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
with expired_pairings as (
    select id
    from pairing_requests
    where metadata_purge_at <= $1
      and (approved_at is null or claimed_at is not null or claim_invalidated_at is not null)
    order by metadata_purge_at, id
    for update skip locked
    limit 100
)
delete from pairing_requests as pairing
using expired_pairings
where pairing.id = expired_pairings.id`, now)
		if err != nil {
			return err
		}
		result.PairingRows = pairings.RowsAffected()
		idempotency, err := tx.Exec(ctx, `
with expired_idempotency as (
    select scope_id, operation, key_hash
    from idempotency_records
    where expires_at <= clock_timestamp()
    order by expires_at, scope_id, operation, key_hash
    for update skip locked
    limit 100
)
delete from idempotency_records as record
using expired_idempotency
where record.scope_id = expired_idempotency.scope_id
  and record.operation = expired_idempotency.operation
  and record.key_hash = expired_idempotency.key_hash`)
		if err != nil {
			return err
		}
		result.IdempotencyRows = idempotency.RowsAffected()
		eventRows, err := tx.Query(ctx, `
with expired_events as (
    select workspace_id, sequence
    from workspace_events
    where expires_at <= $1
    order by expires_at, workspace_id, sequence
    for update skip locked
    limit 100
), deleted_events as (
    delete from workspace_events as event
    using expired_events
    where event.workspace_id = expired_events.workspace_id
      and event.sequence = expired_events.sequence
    returning event.workspace_id, event.sequence
), floors as (
    select workspace_id, max(sequence) as sequence, count(*) as event_count
    from deleted_events
    group by workspace_id
)
select workspace_id::text, sequence, event_count from floors`, now)
		if err != nil {
			return err
		}
		type eventFloor struct {
			workspaceID string
			floor       int64
			count       int64
		}
		floors := make([]eventFloor, 0)
		for eventRows.Next() {
			var workspaceID string
			var floor, count int64
			if err := eventRows.Scan(&workspaceID, &floor, &count); err != nil {
				eventRows.Close()
				return err
			}
			floors = append(floors, eventFloor{workspaceID: workspaceID, floor: floor, count: count})
		}
		if err := eventRows.Err(); err != nil {
			eventRows.Close()
			return err
		}
		eventRows.Close()
		for _, floor := range floors {
			if _, err := tx.Exec(ctx, `
update workspaces
set event_retention_floor = greatest(event_retention_floor, $2)
where id = $1::uuid`, floor.workspaceID, floor.floor); err != nil {
				return err
			}
			result.EventRows += floor.count
		}
		rateLimits, err := tx.Exec(ctx, `
with expired_rate_limits as (
    select scope, subject_hash
    from rate_limit_buckets
    where expires_at <= $1
    order by expires_at, scope, subject_hash
    for update skip locked
    limit 100
)
delete from rate_limit_buckets as bucket
using expired_rate_limits
where bucket.scope = expired_rate_limits.scope
  and bucket.subject_hash = expired_rate_limits.subject_hash`, now)
		if err != nil {
			return err
		}
		result.RateLimitRows = rateLimits.RowsAffected()
		return nil
	})
	return result, err
}
