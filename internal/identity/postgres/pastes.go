package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/jackc/pgx/v5"
)

func (s *txStore) InsertPaste(ctx context.Context, workspaceID, pasteID string, now time.Time) error {
	command, err := s.tx.Exec(ctx, `
insert into pastes(id, workspace_id, created_at)
values ($1::uuid, $2::uuid, $3)`, pasteID, workspaceID, now)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return identity.ErrInvalid
	}
	return nil
}

func (s *txStore) SetPasteKind(ctx context.Context, workspaceID, pasteID, kind string) error {
	if kind != "text" && kind != "image_bundle" {
		return identity.ErrInvalid
	}
	command, err := s.tx.Exec(ctx, `update pastes set paste_kind = $3 where workspace_id = $1::uuid and id = $2::uuid`, workspaceID, pasteID, kind)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return identity.ErrNotFound
	}
	return nil
}

func (s *txStore) AppendTextRevision(ctx context.Context, workspaceID, pasteID, revisionID, kind, eventType string, envelope secure.Envelope, createdAt, expiresAt time.Time) (identity.TextRevision, error) {
	var exists bool
	if err := s.tx.QueryRow(ctx, `
select exists(
    select 1 from pastes where workspace_id = $1::uuid and id = $2::uuid
)`, workspaceID, pasteID).Scan(&exists); err != nil {
		return identity.TextRevision{}, err
	}
	if !exists {
		return identity.TextRevision{}, identity.ErrNotFound
	}
	var sequence int64
	if err := s.tx.QueryRow(ctx, `
update workspaces set next_event_sequence = next_event_sequence + 1
where id = $1::uuid
returning next_event_sequence`, workspaceID).Scan(&sequence); err != nil {
		return identity.TextRevision{}, err
	}
	var keyID any
	var nonce any
	var ciphertext any
	if kind == "content" {
		keyID, nonce, ciphertext = envelope.KeyID, envelope.Nonce, envelope.Ciphertext
	} else if kind != "tombstone" {
		return identity.TextRevision{}, identity.ErrInvalid
	}
	if eventType != "paste.created" && eventType != "paste.revised" && eventType != "paste.deleted" {
		return identity.TextRevision{}, identity.ErrInvalid
	}
	if _, err := s.tx.Exec(ctx, `
insert into paste_revisions(
    id, workspace_id, paste_id, server_sequence, revision_kind,
    text_key_id, text_nonce, text_ciphertext, created_at, expires_at
) values ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9, $10)`,
		revisionID, workspaceID, pasteID, sequence, kind,
		keyID, nonce, ciphertext, createdAt, expiresAt,
	); err != nil {
		return identity.TextRevision{}, err
	}
	if _, err := s.tx.Exec(ctx, `
insert into workspace_events(workspace_id, sequence, event_type, object_id, created_at, expires_at)
values ($1::uuid, $2, $3, $4::uuid, $5, $6)`,
		workspaceID, sequence, eventType, pasteID, createdAt, createdAt.Add(identity.EventLifetime)); err != nil {
		return identity.TextRevision{}, err
	}
	return identity.TextRevision{
		WorkspaceID: workspaceID, PasteID: pasteID, RevisionID: revisionID,
		RevisionKind: kind, ServerSequence: sequence, CreatedAt: createdAt,
		ExpiresAt: expiresAt, Envelope: envelope,
	}, nil
}

func (s *txStore) AppendImageRevision(ctx context.Context, workspaceID, pasteID, revisionID, eventType string, assets []identity.ImageAsset, createdAt, expiresAt time.Time) (identity.TextRevision, error) {
	var exists bool
	if err := s.tx.QueryRow(ctx, `select exists(select 1 from pastes where workspace_id = $1::uuid and id = $2::uuid)`, workspaceID, pasteID).Scan(&exists); err != nil {
		return identity.TextRevision{}, err
	}
	if !exists || len(assets) == 0 {
		return identity.TextRevision{}, identity.ErrNotFound
	}
	if eventType != "paste.created" && eventType != "paste.revised" {
		return identity.TextRevision{}, identity.ErrInvalid
	}
	var sequence int64
	if err := s.tx.QueryRow(ctx, `update workspaces set next_event_sequence = next_event_sequence + 1 where id = $1::uuid returning next_event_sequence`, workspaceID).Scan(&sequence); err != nil {
		return identity.TextRevision{}, err
	}
	if _, err := s.tx.Exec(ctx, `insert into paste_revisions(id,workspace_id,paste_id,server_sequence,revision_kind,created_at,expires_at) values ($1::uuid,$2::uuid,$3::uuid,$4,'image_bundle',$5,$6)`, revisionID, workspaceID, pasteID, sequence, createdAt, expiresAt); err != nil {
		return identity.TextRevision{}, err
	}
	for _, asset := range assets {
		if _, err := s.tx.Exec(ctx, `insert into paste_assets(workspace_id,paste_id,revision_id,asset_index,mime_type,width,height,byte_size,storage_key,image_key_id,image_nonce,created_at,expires_at) values ($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, workspaceID, pasteID, revisionID, asset.AssetIndex, asset.MIMEType, asset.Width, asset.Height, asset.ByteSize, asset.StorageKey, asset.Envelope.KeyID, asset.Envelope.Nonce, createdAt, expiresAt); err != nil {
			return identity.TextRevision{}, err
		}
	}
	if _, err := s.tx.Exec(ctx, `insert into workspace_events(workspace_id,sequence,event_type,object_id,created_at,expires_at) values ($1::uuid,$2,$3,$4::uuid,$5,$6)`, workspaceID, sequence, eventType, pasteID, createdAt, createdAt.Add(identity.EventLifetime)); err != nil {
		return identity.TextRevision{}, err
	}
	return identity.TextRevision{WorkspaceID: workspaceID, PasteID: pasteID, RevisionID: revisionID, RevisionKind: "image_bundle", ServerSequence: sequence, CreatedAt: createdAt, ExpiresAt: expiresAt, Assets: assets}, nil
}

func (s *txStore) ListImageAssets(ctx context.Context, workspaceID, pasteID, revisionID string) ([]identity.ImageAsset, error) {
	rows, err := s.tx.Query(ctx, `select asset_index,mime_type,width,height,byte_size,expires_at,storage_key,image_key_id,image_nonce from paste_assets where workspace_id=$1::uuid and paste_id=$2::uuid and ($3 = '' or revision_id=$3::uuid) order by asset_index`, workspaceID, pasteID, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assets := make([]identity.ImageAsset, 0)
	for rows.Next() {
		var asset identity.ImageAsset
		if err := rows.Scan(&asset.AssetIndex, &asset.MIMEType, &asset.Width, &asset.Height, &asset.ByteSize, &asset.ExpiresAt, &asset.StorageKey, &asset.Envelope.KeyID, &asset.Envelope.Nonce); err != nil {
			return nil, err
		}
		asset.WorkspaceID, asset.PasteID, asset.RevisionID = workspaceID, pasteID, revisionID
		assets = append(assets, asset)
	}
	return assets, rows.Err()
}

func (s *txStore) ListPastes(ctx context.Context, workspaceID string, cutoff, now time.Time) ([]identity.TextRevision, error) {
	rows, err := s.tx.Query(ctx, textRevisionSelect+`
from (
    select distinct on (r.workspace_id, r.paste_id) r.*
    from paste_revisions r
    where r.workspace_id = $1::uuid
    order by r.workspace_id, r.paste_id, r.server_sequence desc
) latest
	where latest.revision_kind in ('content', 'image_bundle')
  and latest.created_at >= $2
  and latest.expires_at > $3
order by latest.server_sequence desc`, workspaceID, cutoff, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]identity.TextRevision, 0)
	for rows.Next() {
		item, err := scanTextRevision(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *txStore) LatestPaste(ctx context.Context, workspaceID string, now time.Time) (identity.LatestPaste, error) {
	row := s.tx.QueryRow(ctx, textRevisionSelect+`
from (
    select distinct on (r.workspace_id, r.paste_id) r.*
    from paste_revisions r
    where r.workspace_id = $1::uuid
    order by r.workspace_id, r.paste_id, r.server_sequence desc
) latest
where latest.revision_kind in ('content', 'image_bundle')
  and latest.expires_at > $2
order by latest.server_sequence desc

limit 1`, workspaceID, now)
	item, err := scanTextRevision(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.LatestPaste{}, identity.ErrNotFound
	}
	if err != nil {
		return identity.LatestPaste{}, err
	}
	latest := identity.LatestPaste{
		Available: true, PasteID: item.PasteID, RevisionID: item.RevisionID,
		ServerSequence: item.ServerSequence, CreatedAt: item.CreatedAt, ExpiresAt: item.ExpiresAt,
		Envelope: item.Envelope,
	}
	if item.RevisionKind == "image_bundle" {
		latest.Images, err = s.ListImageAssets(ctx, item.WorkspaceID, item.PasteID, item.RevisionID)
		if err != nil {
			return identity.LatestPaste{}, err
		}
	}
	return latest, nil
}

func (s *txStore) TouchPaste(ctx context.Context, workspaceID, pasteID string, now time.Time) error {
	command, err := s.tx.Exec(ctx, `
update pastes
set last_mcp_access_at = $3, mcp_access_count = mcp_access_count + 1
where workspace_id = $1::uuid and id = $2::uuid`, workspaceID, pasteID, now)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return identity.ErrNotFound
	}
	return nil
}

const textRevisionSelect = `
select latest.workspace_id::text, latest.paste_id::text, latest.id::text,
       latest.revision_kind, latest.server_sequence, latest.created_at,
       latest.expires_at, latest.text_key_id, latest.text_nonce, latest.text_ciphertext `

type textRevisionRow interface {
	Scan(...any) error
}

func scanTextRevision(row textRevisionRow) (identity.TextRevision, error) {
	var item identity.TextRevision
	var keyID *string
	var nonce, ciphertext []byte
	if err := row.Scan(
		&item.WorkspaceID, &item.PasteID, &item.RevisionID, &item.RevisionKind,
		&item.ServerSequence, &item.CreatedAt, &item.ExpiresAt, &keyID, &nonce, &ciphertext,
	); err != nil {
		return identity.TextRevision{}, err
	}
	if keyID != nil {
		item.Envelope = secure.Envelope{KeyID: *keyID, Nonce: nonce, Ciphertext: ciphertext}
	}
	return item, nil
}

func (s *Store) PurgeText(ctx context.Context, now time.Time) (int64, int64, error) {
	var revisions, pastes int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `
delete from paste_revisions where expires_at <= $1 and revision_kind in ('content', 'tombstone')`, now)
		if err != nil {
			return err
		}
		revisions = command.RowsAffected()
		command, err = tx.Exec(ctx, `
delete from pastes p
where not exists (select 1 from paste_revisions r where r.workspace_id = p.workspace_id and r.paste_id = p.id)`)
		if err != nil {
			return err
		}
		pastes = command.RowsAffected()
		return nil
	})
	return revisions, pastes, err
}

func (s *Store) PurgeImages(ctx context.Context, now time.Time) (int64, int64, error) {
	var revisions, assets int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `delete from paste_assets where expires_at <= $1`, now)
		if err != nil {
			return err
		}
		assets = command.RowsAffected()
		command, err = tx.Exec(ctx, `delete from paste_revisions where expires_at <= $1 and revision_kind = 'image_bundle'`, now)
		if err != nil {
			return err
		}
		revisions = command.RowsAffected()
		_, err = tx.Exec(ctx, `delete from pastes p where not exists (select 1 from paste_revisions r where r.workspace_id = p.workspace_id and r.paste_id = p.id)`)
		return err
	})
	return revisions, assets, err
}

func (s *Store) ListExpiredImages(ctx context.Context, now time.Time) ([]identity.ImageAsset, error) {
	rows, err := s.pool.Query(ctx, `select workspace_id::text,paste_id::text,revision_id::text,asset_index,mime_type,width,height,byte_size,expires_at,storage_key,image_key_id,image_nonce from paste_assets where expires_at <= $1 order by workspace_id,paste_id,revision_id,asset_index`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assets := make([]identity.ImageAsset, 0)
	for rows.Next() {
		var asset identity.ImageAsset
		if err := rows.Scan(&asset.WorkspaceID, &asset.PasteID, &asset.RevisionID, &asset.AssetIndex, &asset.MIMEType, &asset.Width, &asset.Height, &asset.ByteSize, &asset.ExpiresAt, &asset.StorageKey, &asset.Envelope.KeyID, &asset.Envelope.Nonce); err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	return assets, rows.Err()
}
