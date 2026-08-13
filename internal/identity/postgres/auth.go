package postgres

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/jackc/pgx/v5"
)

func (s *Store) Authenticate(ctx context.Context, workspaceID, locator string, presentedHash []byte, now time.Time) (identity.Principal, error) {
	var principal identity.Principal
	var storedHash []byte
	err := s.pool.QueryRow(ctx, `
select c.workspace_id::text, c.device_id::text, c.scope, c.secret_hash
from credentials c
join devices d on d.workspace_id = c.workspace_id and d.id = c.device_id
where c.workspace_id = $1::uuid and c.token_id = $2
  and c.revoked_at is null and d.revoked_at is null`, workspaceID, locator).Scan(
		&principal.WorkspaceID, &principal.DeviceID, &principal.Scope, &storedHash,
	)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && subtle.ConstantTimeCompare(storedHash, presentedHash) != 1 {
		return identity.Principal{}, identity.ErrUnauthorized
	}
	if err != nil {
		return identity.Principal{}, err
	}
	_, err = s.pool.Exec(ctx, `
update credentials set last_used_at = $3
where workspace_id = $1::uuid and token_id = $2 and revoked_at is null`, workspaceID, locator, now)
	if err != nil {
		return identity.Principal{}, err
	}
	return principal, nil
}
