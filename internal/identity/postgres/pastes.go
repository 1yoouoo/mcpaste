package postgres

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/1yoouoo/mcpaste/internal/images"
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
	command, err := s.tx.Exec(ctx, `
update pastes
set paste_kind = $3
where workspace_id = $1::uuid
  and id = $2::uuid
  and (paste_kind = $3 or not exists (
      select 1 from paste_revisions
      where workspace_id = $1::uuid and paste_id = $2::uuid
  ))`, workspaceID, pasteID, kind)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		var exists bool
		if err := s.tx.QueryRow(ctx, `select exists(select 1 from pastes where workspace_id = $1::uuid and id = $2::uuid)`, workspaceID, pasteID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return identity.ErrInvalid
		}
		return identity.ErrNotFound
	}
	return nil
}

func (s *txStore) AppendTextRevision(ctx context.Context, workspaceID, pasteID, revisionID, kind, eventType string, envelope secure.Envelope, createdAt, expiresAt time.Time) (identity.TextRevision, error) {
	var lockedWorkspaceID string
	lockErr := s.tx.QueryRow(ctx, `
select id::text from workspaces
where id = $1::uuid
for update`, workspaceID).Scan(&lockedWorkspaceID)
	if errors.Is(lockErr, pgx.ErrNoRows) {
		return identity.TextRevision{}, identity.ErrNotFound
	}
	if lockErr != nil {
		return identity.TextRevision{}, lockErr
	}
	var exists bool
	var pasteKind string
	if err := s.tx.QueryRow(ctx, `
	select exists(select 1 from pastes where workspace_id = $1::uuid and id = $2::uuid), coalesce((select paste_kind from pastes where workspace_id = $1::uuid and id = $2::uuid), '')`, workspaceID, pasteID).Scan(&exists, &pasteKind); err != nil {
		return identity.TextRevision{}, err
	}
	if !exists {
		return identity.TextRevision{}, identity.ErrNotFound
	}
	if kind == "content" && pasteKind == "image_bundle" {
		return identity.TextRevision{}, identity.ErrInvalid
	}
	var latestKind string
	latestErr := s.tx.QueryRow(ctx, `select revision_kind from paste_revisions where workspace_id=$1::uuid and paste_id=$2::uuid order by server_sequence desc limit 1`, workspaceID, pasteID).Scan(&latestKind)
	if latestErr != nil && !errors.Is(latestErr, pgx.ErrNoRows) {
		return identity.TextRevision{}, latestErr
	}
	if latestErr == nil && latestKind == identity.RevisionTombstone {
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
	var latestKind *string
	latestErr := s.tx.QueryRow(ctx, `select revision_kind from paste_revisions where workspace_id=$1::uuid and paste_id=$2::uuid order by server_sequence desc limit 1`, workspaceID, pasteID).Scan(&latestKind)
	if latestErr != nil && !errors.Is(latestErr, pgx.ErrNoRows) {
		return identity.TextRevision{}, latestErr
	}
	if latestErr == nil && latestKind != nil && *latestKind == "tombstone" {
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

func (s *txStore) AppendAttachmentRevision(ctx context.Context, workspaceID, pasteID, revisionID, eventType string, assets []identity.ImageAsset, createdAt, expiresAt time.Time) (identity.TextRevision, error) {
	if len(assets) > images.MaxAttachmentItems || eventType != "paste.revised" {
		return identity.TextRevision{}, identity.ErrInvalid
	}
	seenIndexes := make([]bool, len(assets))
	for _, asset := range assets {
		if asset.AssetIndex < 0 || asset.AssetIndex >= len(assets) || seenIndexes[asset.AssetIndex] {
			return identity.TextRevision{}, identity.ErrInvalid
		}
		seenIndexes[asset.AssetIndex] = true
	}
	var lockedWorkspaceID string
	lockErr := s.tx.QueryRow(ctx, `
select id::text from workspaces
where id = $1::uuid
for update`, workspaceID).Scan(&lockedWorkspaceID)
	if errors.Is(lockErr, pgx.ErrNoRows) {
		return identity.TextRevision{}, identity.ErrNotFound
	}
	if lockErr != nil {
		return identity.TextRevision{}, lockErr
	}
	var latestKind string
	latestErr := s.tx.QueryRow(ctx, `select revision_kind from paste_revisions where workspace_id=$1::uuid and paste_id=$2::uuid order by server_sequence desc limit 1`, workspaceID, pasteID).Scan(&latestKind)
	if errors.Is(latestErr, pgx.ErrNoRows) {
		return identity.TextRevision{}, identity.ErrNotFound
	}
	if latestErr != nil {
		return identity.TextRevision{}, latestErr
	}
	if latestKind == identity.RevisionTombstone {
		return identity.TextRevision{}, identity.ErrNotFound
	}

	var sequence int64
	if err := s.tx.QueryRow(ctx, `update workspaces set next_event_sequence = next_event_sequence + 1 where id = $1::uuid returning next_event_sequence`, workspaceID).Scan(&sequence); err != nil {
		return identity.TextRevision{}, err
	}
	if _, err := s.tx.Exec(ctx, `insert into paste_revisions(id,workspace_id,paste_id,server_sequence,revision_kind,created_at,expires_at) values ($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7)`, revisionID, workspaceID, pasteID, sequence, identity.RevisionAttachmentBundle, createdAt, expiresAt); err != nil {
		return identity.TextRevision{}, err
	}
	copiedAssets := make([]identity.ImageAsset, len(assets))
	for _, asset := range assets {
		if _, err := s.tx.Exec(ctx, `insert into paste_assets(workspace_id,paste_id,revision_id,asset_index,mime_type,width,height,byte_size,storage_key,image_key_id,image_nonce,created_at,expires_at) values ($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, workspaceID, pasteID, revisionID, asset.AssetIndex, asset.MIMEType, asset.Width, asset.Height, asset.ByteSize, asset.StorageKey, asset.Envelope.KeyID, asset.Envelope.Nonce, createdAt, expiresAt); err != nil {
			return identity.TextRevision{}, err
		}
		copiedAsset := asset
		copiedAsset.WorkspaceID = workspaceID
		copiedAsset.PasteID = pasteID
		copiedAsset.RevisionID = revisionID
		copiedAsset.ExpiresAt = expiresAt
		copiedAsset.Envelope.Nonce = bytes.Clone(asset.Envelope.Nonce)
		copiedAsset.Envelope.Ciphertext = bytes.Clone(asset.Envelope.Ciphertext)
		copiedAsset.Bytes = bytes.Clone(asset.Bytes)
		copiedAssets[asset.AssetIndex] = copiedAsset
	}
	if _, err := s.tx.Exec(ctx, `insert into workspace_events(workspace_id,sequence,event_type,object_id,created_at,expires_at) values ($1::uuid,$2,$3,$4::uuid,$5,$6)`, workspaceID, sequence, eventType, pasteID, createdAt, createdAt.Add(identity.EventLifetime)); err != nil {
		return identity.TextRevision{}, err
	}
	return identity.TextRevision{
		WorkspaceID: workspaceID, PasteID: pasteID, RevisionID: revisionID,
		RevisionKind: identity.RevisionAttachmentBundle, ServerSequence: sequence,
		CreatedAt: createdAt, ExpiresAt: expiresAt, Assets: copiedAssets,
	}, nil
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

func (s *txStore) Snapshot(ctx context.Context, workspaceID string, now time.Time) (identity.SnapshotResult, error) {
	var cursor int64
	if err := s.tx.QueryRow(ctx, `select next_event_sequence from workspaces where id=$1::uuid`, workspaceID).Scan(&cursor); err != nil {
		return identity.SnapshotResult{}, err
	}
	revisions, err := s.ListPastes(ctx, workspaceID, time.Unix(0, 0).UTC(), now)
	if err != nil {
		return identity.SnapshotResult{}, err
	}
	return identity.SnapshotResult{Cursor: cursor, Revisions: revisions}, nil
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
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, cleanupAdvisoryLockName); err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `
with expired_revisions as (
    select id
    from paste_revisions
    where expires_at <= $1 and revision_kind in ('content', 'tombstone')
    order by expires_at, id
    for update skip locked
    limit 100
)
delete from paste_revisions as revision
using expired_revisions
where revision.id = expired_revisions.id`, now)
		if err != nil {
			return err
		}
		revisions = command.RowsAffected()
		command, err = tx.Exec(ctx, `
with orphan_pastes as (
    select p.workspace_id, p.id
    from pastes p
    where not exists (select 1 from paste_revisions r where r.workspace_id = p.workspace_id and r.paste_id = p.id)
    order by p.workspace_id, p.id
    for update skip locked
    limit 100
)
delete from pastes as paste
using orphan_pastes
where paste.workspace_id = orphan_pastes.workspace_id
  and paste.id = orphan_pastes.id`)
		if err != nil {
			return err
		}
		pastes = command.RowsAffected()
		return nil
	})
	return revisions, pastes, err
}

func (s *Store) PurgeImageRevisions(ctx context.Context, now time.Time, expired []identity.ExpiredImageRevision) (int64, int64, error) {
	if len(expired) == 0 {
		return 0, 0, nil
	}
	workspaceIDs := make([]string, 0, len(expired))
	pasteIDs := make([]string, 0, len(expired))
	revisionIDs := make([]string, 0, len(expired))
	for _, revision := range expired {
		workspaceIDs = append(workspaceIDs, revision.WorkspaceID)
		pasteIDs = append(pasteIDs, revision.PasteID)
		revisionIDs = append(revisionIDs, revision.RevisionID)
	}
	var revisions, assets int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, cleanupAdvisoryLockName); err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `
with selected_revisions as (
    select workspace_id, paste_id, revision_id
    from unnest($2::uuid[], $3::uuid[], $4::uuid[])
         as selected(workspace_id, paste_id, revision_id)
),
eligible_revisions as (
    select selected.workspace_id, selected.paste_id, selected.revision_id
    from selected_revisions as selected
    join paste_revisions as revision
      on revision.workspace_id = selected.workspace_id
     and revision.paste_id = selected.paste_id
     and revision.id = selected.revision_id
    where revision.expires_at <= $1
      and revision.revision_kind in ('image_bundle', 'attachment_bundle')
)
delete from paste_assets as asset
using eligible_revisions as selected
where asset.workspace_id = selected.workspace_id
  and asset.paste_id = selected.paste_id
  and asset.revision_id = selected.revision_id`, now, workspaceIDs, pasteIDs, revisionIDs)
		if err != nil {
			return err
		}
		assets = command.RowsAffected()
		rows, err := tx.Query(ctx, `
with selected_revisions as (
    select workspace_id, paste_id, revision_id
    from unnest($2::uuid[], $3::uuid[], $4::uuid[])
         as selected(workspace_id, paste_id, revision_id)
)
delete from paste_revisions as revision
using selected_revisions as selected
where revision.workspace_id = selected.workspace_id
  and revision.paste_id = selected.paste_id
  and revision.id = selected.revision_id
  and revision.expires_at <= $1
  and revision.revision_kind in ('image_bundle', 'attachment_bundle')
returning revision.workspace_id::text, revision.paste_id::text`, now, workspaceIDs, pasteIDs, revisionIDs)
		if err != nil {
			return err
		}
		deletedWorkspaceIDs := make([]string, 0, len(expired))
		deletedPasteIDs := make([]string, 0, len(expired))
		for rows.Next() {
			var workspaceID, pasteID string
			if err := rows.Scan(&workspaceID, &pasteID); err != nil {
				rows.Close()
				return err
			}
			deletedWorkspaceIDs = append(deletedWorkspaceIDs, workspaceID)
			deletedPasteIDs = append(deletedPasteIDs, pasteID)
			revisions++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(deletedWorkspaceIDs) == 0 {
			return nil
		}
		_, err = tx.Exec(ctx, `
with selected_pastes as (
    select distinct workspace_id, paste_id
    from unnest($1::uuid[], $2::uuid[]) as selected(workspace_id, paste_id)
)
delete from pastes as paste
using selected_pastes as selected
where paste.workspace_id = selected.workspace_id
  and paste.id = selected.paste_id
  and not exists (
      select 1
      from paste_revisions as revision
      where revision.workspace_id = paste.workspace_id
        and revision.paste_id = paste.id
  )`, deletedWorkspaceIDs, deletedPasteIDs)
		if err != nil {
			return err
		}
		return nil
	})
	return revisions, assets, err
}

func (s *Store) ListExpiredImageRevisions(ctx context.Context, now time.Time, limit int) ([]identity.ExpiredImageRevision, error) {
	if limit <= 0 {
		return []identity.ExpiredImageRevision{}, nil
	}
	rows, err := s.pool.Query(ctx, `
with selected_revisions as (
    select workspace_id, paste_id, id, expires_at
    from paste_revisions
    where expires_at <= $1
      and revision_kind in ('image_bundle', 'attachment_bundle')
    order by expires_at, workspace_id, paste_id, id
    limit $2
)
select revision.workspace_id::text, revision.paste_id::text, revision.id::text,
       asset.id is not null,
       coalesce(asset.asset_index, 0), coalesce(asset.mime_type, ''),
       coalesce(asset.width, 0), coalesce(asset.height, 0), coalesce(asset.byte_size, 0),
       coalesce(asset.expires_at, 'epoch'::timestamptz), coalesce(asset.storage_key, ''),
       coalesce(asset.image_key_id, ''), coalesce(asset.image_nonce, ''::bytea)
from selected_revisions as revision
left join paste_assets as asset
  on asset.workspace_id = revision.workspace_id
 and asset.paste_id = revision.paste_id
 and asset.revision_id = revision.id
order by revision.expires_at, revision.workspace_id, revision.paste_id, revision.id, asset.asset_index`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	revisions := make([]identity.ExpiredImageRevision, 0)
	for rows.Next() {
		var workspaceID, pasteID, revisionID string
		var hasAsset bool
		var asset identity.ImageAsset
		if err := rows.Scan(
			&workspaceID, &pasteID, &revisionID, &hasAsset,
			&asset.AssetIndex, &asset.MIMEType, &asset.Width, &asset.Height, &asset.ByteSize,
			&asset.ExpiresAt, &asset.StorageKey, &asset.Envelope.KeyID, &asset.Envelope.Nonce,
		); err != nil {
			return nil, err
		}
		if len(revisions) == 0 || revisions[len(revisions)-1].RevisionID != revisionID {
			revisions = append(revisions, identity.ExpiredImageRevision{
				WorkspaceID: workspaceID,
				PasteID:     pasteID,
				RevisionID:  revisionID,
				Assets:      []identity.ImageAsset{},
			})
		}
		if hasAsset {
			asset.WorkspaceID = workspaceID
			asset.PasteID = pasteID
			asset.RevisionID = revisionID
			index := len(revisions) - 1
			revisions[index].Assets = append(revisions[index].Assets, asset)
		}
	}
	return revisions, rows.Err()
}
