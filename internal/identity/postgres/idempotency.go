package postgres

import (
	"context"
	"errors"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/jackc/pgx/v5"
)

func (s *txStore) GetIdempotency(ctx context.Context, scopeID, operation string, keyHash []byte) (identity.IdempotencyRecord, error) {
	var record identity.IdempotencyRecord
	var workspaceID *string
	err := s.tx.QueryRow(ctx, `
select scope_id, operation, key_hash, workspace_id::text, request_hash,
       response_status, response_content_type, response_key_id,
       response_nonce, response_ciphertext, created_at, expires_at,
       expires_at <= clock_timestamp()
from idempotency_records
where scope_id = $1 and operation = $2 and key_hash = $3
for update`, scopeID, operation, keyHash).Scan(
		&record.ScopeID, &record.Operation, &record.KeyHash, &workspaceID, &record.RequestHash,
		&record.Response.Status, &record.Response.ContentType, &record.Response.Envelope.KeyID,
		&record.Response.Envelope.Nonce, &record.Response.Envelope.Ciphertext,
		&record.CreatedAt, &record.ExpiresAt, &record.Expired,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.IdempotencyRecord{}, identity.ErrNotFound
	}
	if err != nil {
		return identity.IdempotencyRecord{}, err
	}
	if workspaceID != nil {
		record.WorkspaceID = *workspaceID
	}
	return record, nil
}

func (s *txStore) DeleteIdempotency(ctx context.Context, scopeID, operation string, keyHash []byte) error {
	command, err := s.tx.Exec(ctx, `
delete from idempotency_records
where scope_id = $1 and operation = $2 and key_hash = $3`, scopeID, operation, keyHash)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return identity.ErrNotFound
	}
	return nil
}

func (s *txStore) PutIdempotency(ctx context.Context, record identity.IdempotencyRecord) error {
	var workspaceID any
	if record.WorkspaceID != "" {
		workspaceID = record.WorkspaceID
	}
	_, err := s.tx.Exec(ctx, `
insert into idempotency_records(
    scope_id, operation, key_hash, workspace_id, request_hash,
    response_status, response_content_type, response_key_id,
    response_nonce, response_ciphertext, created_at, expires_at
) select $1, $2, $3, $4::uuid, $5, $6, $7, $8, $9, $10,
         stamp.created_at, stamp.created_at + interval '24 hours'
  from (select clock_timestamp() as created_at) stamp`,
		record.ScopeID, record.Operation, record.KeyHash, workspaceID, record.RequestHash,
		record.Response.Status, record.Response.ContentType, record.Response.Envelope.KeyID,
		record.Response.Envelope.Nonce, record.Response.Envelope.Ciphertext,
	)
	return err
}
