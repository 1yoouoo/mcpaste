package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/jackc/pgx/v5"
)

func (s *txStore) Sync(ctx context.Context, workspaceID string, after int64, limit int, now time.Time) (identity.SyncResult, error) {
	if after < 0 || limit < 1 {
		return identity.SyncResult{}, identity.ErrInvalid
	}
	var cursor, retentionFloor int64
	if err := s.tx.QueryRow(ctx, `
select next_event_sequence, event_retention_floor from workspaces where id = $1::uuid`, workspaceID).Scan(&cursor, &retentionFloor); errors.Is(err, pgx.ErrNoRows) {
		return identity.SyncResult{}, identity.ErrNotFound
	} else if err != nil {
		return identity.SyncResult{}, err
	}
	if after > 0 && after < retentionFloor {
		return identity.SyncResult{}, identity.ErrCursorExpired
	}
	rows, err := s.tx.Query(ctx, `
select e.sequence, e.event_type, r.workspace_id::text, r.paste_id::text, r.id::text,
       r.revision_kind, r.server_sequence, r.created_at, r.expires_at,
       r.text_key_id, r.text_nonce, r.text_ciphertext
from workspace_events e
join paste_revisions r on r.workspace_id = e.workspace_id and r.server_sequence = e.sequence
where e.workspace_id = $1::uuid
  and e.sequence > $2
  and e.event_type in ('paste.created', 'paste.revised', 'paste.deleted')
  and (
      r.revision_kind not in ('image_bundle', 'attachment_bundle')
      or r.expires_at > $4
  )
order by e.sequence
limit $3`, workspaceID, after, limit+1, now)
	if err != nil {
		return identity.SyncResult{}, err
	}
	defer rows.Close()
	events := make([]identity.SyncEvent, 0, limit)
	for rows.Next() {
		var event identity.SyncEvent
		var keyID *string
		var nonce, ciphertext []byte
		if err := rows.Scan(
			&event.Sequence, &event.EventType, &event.WorkspaceID, &event.PasteID, &event.RevisionID,
			&event.RevisionKind, &event.ServerSequence, &event.CreatedAt, &event.ExpiresAt,
			&keyID, &nonce, &ciphertext,
		); err != nil {
			return identity.SyncResult{}, err
		}
		if keyID != nil {
			event.Envelope.KeyID = *keyID
			event.Envelope.Nonce = nonce
			event.Envelope.Ciphertext = ciphertext
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return identity.SyncResult{}, err
	}
	hasMore := len(events) > limit
	resultCursor := cursor
	if hasMore {
		events = events[:limit]
		resultCursor = events[len(events)-1].Sequence
	}
	return identity.SyncResult{Cursor: resultCursor, HasMore: hasMore, Events: events}, nil
}
