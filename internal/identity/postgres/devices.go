package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/jackc/pgx/v5"
)

func (s *txStore) ListDevices(ctx context.Context, workspaceID, currentDeviceID string) ([]identity.Device, error) {
	rows, err := s.tx.Query(ctx, `
select id::text, display_name, platform, role, created_at, id = $2::uuid
from devices
where workspace_id = $1::uuid and revoked_at is null
order by created_at, id`, workspaceID, currentDeviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	devices := make([]identity.Device, 0)
	for rows.Next() {
		var device identity.Device
		if err := rows.Scan(&device.ID, &device.DisplayName, &device.Platform, &device.Role, &device.CreatedAt, &device.IsCurrent); err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

func (s *txStore) RenameDevice(ctx context.Context, workspaceID, deviceID, requestedName string, now time.Time) (identity.Device, error) {
	if _, err := s.tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended($1, 0))", workspaceID); err != nil {
		return identity.Device{}, err
	}
	var device identity.Device
	err := s.tx.QueryRow(ctx, `
select id::text, display_name, platform, role, created_at
from devices
where workspace_id = $1::uuid and id = $2::uuid and revoked_at is null
for update`, workspaceID, deviceID).Scan(
		&device.ID, &device.DisplayName, &device.Platform, &device.Role, &device.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Device{}, identity.ErrNotFound
	}
	if err != nil {
		return identity.Device{}, err
	}
	collisions, err := s.loadDeviceDisplayNameCollisions(ctx, workspaceID, deviceID)
	if err != nil {
		return identity.Device{}, err
	}
	for attempt := 1; attempt <= 9999; attempt++ {
		candidate := identity.DisplayNameCandidate(requestedName, attempt)
		exists, err := s.deviceDisplayNameCollides(ctx, collisions, candidate)
		if err != nil {
			return identity.Device{}, err
		}
		if exists {
			continue
		}
		if _, err := s.tx.Exec(ctx, `
update devices set display_name = $3
where workspace_id = $1::uuid and id = $2::uuid and revoked_at is null`, workspaceID, deviceID, candidate); err != nil {
			return identity.Device{}, err
		}
		device.DisplayName = candidate
		return device, nil
	}
	return identity.Device{}, identity.ErrInvalid
}

func (s *txStore) RevokeDevice(ctx context.Context, workspaceID, deviceID string, now time.Time) error {
	command, err := s.tx.Exec(ctx, `
update devices set revoked_at = $3
where workspace_id = $1::uuid and id = $2::uuid and revoked_at is null`, workspaceID, deviceID, now)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return identity.ErrNotFound
	}
	_, err = s.tx.Exec(ctx, `
update credentials set revoked_at = $3
where workspace_id = $1::uuid and device_id = $2::uuid and revoked_at is null`, workspaceID, deviceID, now)
	return err
}

func (s *txStore) InsertEvent(ctx context.Context, workspaceID, eventType, objectID string, now time.Time) error {
	var sequence int64
	if err := s.tx.QueryRow(ctx, `
update workspaces set next_event_sequence = next_event_sequence + 1
where id = $1::uuid
returning next_event_sequence`, workspaceID).Scan(&sequence); err != nil {
		return err
	}
	_, err := s.tx.Exec(ctx, `
insert into workspace_events(workspace_id, sequence, event_type, object_id, created_at, expires_at)
values ($1::uuid, $2, $3, $4::uuid, $5, $6)`,
		workspaceID, sequence, eventType, objectID, now, now.Add(identity.EventLifetime),
	)
	return err
}
