package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/jackc/pgx/v5"
	"golang.org/x/text/cases"
)

func (s *txStore) InsertWorkspace(ctx context.Context, workspaceID string, createdAt time.Time) error {
	_, err := s.tx.Exec(ctx, "insert into workspaces(id, created_at) values ($1::uuid, $2)", workspaceID, createdAt)
	return err
}

func (s *txStore) InsertDevice(ctx context.Context, workspaceID string, device identity.Device) (identity.Device, error) {
	if _, err := s.tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended($1, 0))", workspaceID); err != nil {
		return identity.Device{}, err
	}
	collisions, err := s.loadDeviceDisplayNameCollisions(ctx, workspaceID, "")
	if err != nil {
		return identity.Device{}, err
	}
	for attempt := 1; attempt <= 9999; attempt++ {
		candidate := identity.DisplayNameCandidate(device.DisplayName, attempt)
		exists, err := s.deviceDisplayNameCollides(ctx, collisions, candidate)
		if err != nil {
			return identity.Device{}, err
		}
		if exists {
			continue
		}
		_, err = s.tx.Exec(ctx, `
insert into devices(id, workspace_id, display_name, platform, role, created_at)
values ($1::uuid, $2::uuid, $3, $4, $5, $6)`,
			device.ID, workspaceID, candidate, device.Platform, device.Role, device.CreatedAt,
		)
		if err != nil {
			return identity.Device{}, err
		}
		device.DisplayName = candidate
		return device, nil
	}
	return identity.Device{}, identity.ErrInvalid
}

type deviceDisplayNameCollisionSet struct {
	folded  map[string]struct{}
	lowered map[string]struct{}
}

func (s *txStore) loadDeviceDisplayNameCollisions(ctx context.Context, workspaceID, excludedDeviceID string) (deviceDisplayNameCollisionSet, error) {
	rows, err := s.tx.Query(ctx, `
select id::text, display_name, lower(display_name)
from devices
where workspace_id = $1::uuid`, workspaceID)
	if err != nil {
		return deviceDisplayNameCollisionSet{}, err
	}
	defer rows.Close()

	folder := cases.Fold()
	collisions := deviceDisplayNameCollisionSet{
		folded:  make(map[string]struct{}),
		lowered: make(map[string]struct{}),
	}
	for rows.Next() {
		var deviceID string
		var displayName string
		var loweredDisplayName string
		if err := rows.Scan(&deviceID, &displayName, &loweredDisplayName); err != nil {
			return deviceDisplayNameCollisionSet{}, err
		}
		if deviceID == excludedDeviceID {
			continue
		}
		collisions.folded[folder.String(displayName)] = struct{}{}
		collisions.lowered[loweredDisplayName] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return deviceDisplayNameCollisionSet{}, err
	}
	return collisions, nil
}

func (s *txStore) deviceDisplayNameCollides(ctx context.Context, collisions deviceDisplayNameCollisionSet, candidate string) (bool, error) {
	var loweredCandidate string
	if err := s.tx.QueryRow(ctx, "select lower($1::text)", candidate).Scan(&loweredCandidate); err != nil {
		return false, err
	}
	_, foldedCollision := collisions.folded[cases.Fold().String(candidate)]
	_, loweredCollision := collisions.lowered[loweredCandidate]
	return foldedCollision || loweredCollision, nil
}

func (s *txStore) InsertCredential(ctx context.Context, workspaceID string, record identity.CredentialRecord) error {
	_, err := s.tx.Exec(ctx, `
insert into credentials(workspace_id, device_id, token_id, scope, secret_hash, created_at)
values ($1::uuid, $2::uuid, $3, $4, $5, $6)`,
		workspaceID, record.DeviceID, record.Locator, record.Scope, record.Hash, record.CreatedAt,
	)
	return err
}

func (s *txStore) GetRecovery(ctx context.Context, workspaceID, locator string) (identity.RecoveryRecord, error) {
	var record identity.RecoveryRecord
	err := s.tx.QueryRow(ctx, `
select workspace_id::text, locator, salt, verifier,
       argon_version, argon_time, argon_memory_kib, argon_threads,
       created_at, rotated_at
from recovery_verifiers
where workspace_id = $1::uuid and locator = $2
for update`, workspaceID, locator).Scan(
		&record.WorkspaceID, &record.Locator,
		&record.Verifier.Salt, &record.Verifier.Hash,
		&record.Verifier.Version, &record.Verifier.Time,
		&record.Verifier.MemoryKiB, &record.Verifier.Threads,
		&record.CreatedAt, &record.RotatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.RecoveryRecord{}, identity.ErrInvalidRecovery
	}
	return record, err
}

func (s *txStore) PutRecovery(ctx context.Context, workspaceID string, record identity.RecoveryRecord) error {
	_, err := s.tx.Exec(ctx, `
insert into recovery_verifiers(
    workspace_id, locator, salt, verifier, argon_version,
    argon_time, argon_memory_kib, argon_threads, created_at, rotated_at
) values ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10)
on conflict (workspace_id) do update set
    locator = excluded.locator,
    salt = excluded.salt,
    verifier = excluded.verifier,
    argon_version = excluded.argon_version,
    argon_time = excluded.argon_time,
    argon_memory_kib = excluded.argon_memory_kib,
    argon_threads = excluded.argon_threads,
    rotated_at = excluded.rotated_at`,
		workspaceID, record.Locator, record.Verifier.Salt, record.Verifier.Hash,
		record.Verifier.Version, record.Verifier.Time, record.Verifier.MemoryKiB,
		record.Verifier.Threads, record.CreatedAt, record.RotatedAt,
	)
	return err
}
