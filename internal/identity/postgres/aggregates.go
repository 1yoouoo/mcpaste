package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/jackc/pgx/v5"
)

func aggregateComponentCTEs(pastePredicate string) string {
	return `
with latest_any as (
    select distinct on (r.paste_id)
           r.workspace_id, r.paste_id, r.id, r.revision_kind,
           r.server_sequence, r.created_at
    from paste_revisions r
    where r.workspace_id = $1::uuid` + pastePredicate + `
    order by r.paste_id, r.server_sequence desc
),
latest_text as (
    select distinct on (r.paste_id)
           r.workspace_id, r.paste_id, r.id, r.server_sequence,
           r.created_at, r.expires_at,
           r.text_key_id, r.text_nonce, r.text_ciphertext
    from paste_revisions r
    where r.workspace_id = $1::uuid` + pastePredicate + `
      and r.revision_kind = $4
      and r.expires_at > $3
    order by r.paste_id, r.server_sequence desc
),
latest_attachments as (
    select distinct on (r.paste_id)
           r.workspace_id, r.paste_id, r.id, r.revision_kind,
           r.server_sequence, r.created_at, r.expires_at
    from paste_revisions r
    where r.workspace_id = $1::uuid` + pastePredicate + `
      and r.revision_kind in ($5, $6)
      and r.expires_at > $3
    order by r.paste_id, r.server_sequence desc
),
aggregate_components as (
    select latest_any.workspace_id::text as workspace_id,
           latest_any.paste_id::text as paste_id,
           (case
               when latest_any.revision_kind = $7 then latest_any.id
               when latest_attachments.server_sequence is not null
                    and (latest_text.server_sequence is null
                         or latest_attachments.server_sequence > latest_text.server_sequence)
                   then latest_attachments.id
               else latest_text.id
           end)::text as aggregate_revision_id,
           case
               when latest_any.revision_kind = $7 then latest_any.server_sequence
               when latest_attachments.server_sequence is not null
                    and (latest_text.server_sequence is null
                         or latest_attachments.server_sequence > latest_text.server_sequence)
                   then latest_attachments.server_sequence
               else latest_text.server_sequence
           end as aggregate_server_sequence,
           case
               when latest_any.revision_kind = $7 then latest_any.created_at
               when latest_attachments.server_sequence is not null
                    and (latest_text.server_sequence is null
                         or latest_attachments.server_sequence > latest_text.server_sequence)
                   then latest_attachments.created_at
               else latest_text.created_at
           end as aggregate_created_at,
           latest_any.revision_kind = $7 as deleted,
           latest_text.id::text as text_revision_id,
           latest_text.server_sequence as text_server_sequence,
           latest_text.created_at as text_created_at,
           latest_text.expires_at as text_expires_at,
           latest_text.text_key_id, latest_text.text_nonce, latest_text.text_ciphertext,
           latest_attachments.id::text as attachment_revision_id,
           latest_attachments.revision_kind as attachment_revision_kind,
           latest_attachments.server_sequence as attachment_server_sequence,
           latest_attachments.created_at as attachment_created_at,
           latest_attachments.expires_at as attachment_expires_at
    from latest_any
    left join latest_text
      on latest_text.workspace_id = latest_any.workspace_id
     and latest_text.paste_id = latest_any.paste_id
    left join latest_attachments
      on latest_attachments.workspace_id = latest_any.workspace_id
     and latest_attachments.paste_id = latest_any.paste_id
    where latest_any.revision_kind = $7
       or latest_text.id is not null
       or latest_attachments.id is not null
)
`
}

func workspaceAggregateComponentCTEs() string {
	return aggregateComponentCTEs("")
}

func directAggregateComponentCTEs() string {
	return aggregateComponentCTEs("\n      and r.paste_id = $2::uuid")
}

const pasteAggregateSelect = `
select workspace_id, paste_id, aggregate_revision_id,
       aggregate_server_sequence, aggregate_created_at, deleted,
       text_revision_id, text_server_sequence, text_created_at, text_expires_at,
       text_key_id, text_nonce, text_ciphertext,
       attachment_revision_id, attachment_revision_kind,
       attachment_server_sequence, attachment_created_at, attachment_expires_at
from aggregate_components
`

func (s *txStore) PasteAggregate(ctx context.Context, workspaceID, pasteID string, now time.Time) (identity.PasteAggregate, error) {
	row := s.tx.QueryRow(ctx, directAggregateComponentCTEs()+pasteAggregateSelect,
		directAggregateQueryArguments(workspaceID, pasteID, now)...)
	aggregate, err := scanPasteAggregate(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.PasteAggregate{}, identity.ErrNotFound
	}
	if err != nil {
		return identity.PasteAggregate{}, err
	}
	aggregates := []identity.PasteAggregate{aggregate}
	if err := s.loadAggregateAssets(ctx, aggregates); err != nil {
		return identity.PasteAggregate{}, err
	}
	return aggregates[0], nil
}

func (s *txStore) ListPasteAggregates(ctx context.Context, workspaceID string, cutoff, now time.Time) ([]identity.PasteAggregate, error) {
	rows, err := s.tx.Query(ctx, workspaceAggregateComponentCTEs()+pasteAggregateSelect+`
where not deleted
  and aggregate_created_at >= $2
order by aggregate_server_sequence desc`, aggregateListQueryArguments(workspaceID, cutoff, now)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	aggregates := make([]identity.PasteAggregate, 0)
	for rows.Next() {
		aggregate, err := scanPasteAggregate(rows)
		if err != nil {
			return nil, err
		}
		aggregates = append(aggregates, aggregate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := s.loadAggregateAssets(ctx, aggregates); err != nil {
		return nil, err
	}
	return aggregates, nil
}

func (s *txStore) SnapshotAggregates(ctx context.Context, workspaceID string, now time.Time) (int64, []identity.PasteAggregate, error) {
	cursor, err := s.snapshotAggregateCursor(ctx, workspaceID)
	if err != nil {
		return 0, nil, err
	}
	aggregates, err := s.ListPasteAggregates(ctx, workspaceID, time.Time{}, now)
	if err != nil {
		return 0, nil, err
	}
	return cursor, aggregates, nil
}

func (s *txStore) snapshotAggregateCursor(ctx context.Context, workspaceID string) (int64, error) {
	var cursor int64
	err := s.tx.QueryRow(ctx, `select next_event_sequence from workspaces
where id = $1::uuid
for share`, workspaceID).Scan(&cursor)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, identity.ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return cursor, nil
}

func (s *txStore) LatestPasteAggregate(ctx context.Context, workspaceID string, now time.Time) (identity.PasteAggregate, error) {
	row := s.tx.QueryRow(ctx, workspaceAggregateComponentCTEs()+pasteAggregateSelect+`
where not deleted
  and aggregate_created_at >= $2
order by aggregate_server_sequence desc
limit 1`, aggregateListQueryArguments(workspaceID, time.Time{}, now)...)
	aggregate, err := scanPasteAggregate(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.PasteAggregate{}, identity.ErrNotFound
	}
	if err != nil {
		return identity.PasteAggregate{}, err
	}
	aggregates := []identity.PasteAggregate{aggregate}
	if err := s.loadAggregateAssets(ctx, aggregates); err != nil {
		return identity.PasteAggregate{}, err
	}
	return aggregates[0], nil
}

func (s *txStore) CurrentAttachmentAsset(ctx context.Context, workspaceID, pasteID string, assetIndex int, now time.Time) (identity.ImageAsset, error) {
	var asset identity.ImageAsset
	err := s.tx.QueryRow(ctx, `
with latest_any as (
    select revision_kind
    from paste_revisions
    where workspace_id = $1::uuid and paste_id = $2::uuid
    order by server_sequence desc
    limit 1
),
current_attachment as (
    select id
    from paste_revisions
    where workspace_id = $1::uuid
      and paste_id = $2::uuid
      and revision_kind in ($5, $6)
      and expires_at > $4
    order by server_sequence desc
    limit 1
)
select asset.workspace_id::text, asset.paste_id::text, asset.revision_id::text,
       asset.asset_index, asset.mime_type, asset.width, asset.height,
       asset.byte_size, asset.expires_at, asset.storage_key,
       asset.image_key_id, asset.image_nonce
from latest_any
join current_attachment on true
join paste_assets asset
  on asset.workspace_id = $1::uuid
 and asset.paste_id = $2::uuid
 and asset.revision_id = current_attachment.id
where latest_any.revision_kind <> $7
  and asset.asset_index = $3`, workspaceID, pasteID, assetIndex, now,
		identity.RevisionAttachmentBundle, identity.RevisionImageBundle, identity.RevisionTombstone,
	).Scan(
		&asset.WorkspaceID, &asset.PasteID, &asset.RevisionID, &asset.AssetIndex,
		&asset.MIMEType, &asset.Width, &asset.Height, &asset.ByteSize, &asset.ExpiresAt,
		&asset.StorageKey, &asset.Envelope.KeyID, &asset.Envelope.Nonce,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.ImageAsset{}, identity.ErrNotFound
	}
	if err != nil {
		return identity.ImageAsset{}, err
	}
	return asset, nil
}

func directAggregateQueryArguments(workspaceID, pasteID string, now time.Time) []any {
	return []any{
		workspaceID, pasteID, now,
		identity.RevisionContent,
		identity.RevisionAttachmentBundle,
		identity.RevisionImageBundle,
		identity.RevisionTombstone,
	}
}

func aggregateListQueryArguments(workspaceID string, cutoff, now time.Time) []any {
	return []any{
		workspaceID, cutoff, now,
		identity.RevisionContent,
		identity.RevisionAttachmentBundle,
		identity.RevisionImageBundle,
		identity.RevisionTombstone,
	}
}

type pasteAggregateRow interface {
	Scan(...any) error
}

func scanPasteAggregate(row pasteAggregateRow) (identity.PasteAggregate, error) {
	var aggregate identity.PasteAggregate
	var workspaceID string
	var textRevisionID, textKeyID *string
	var textServerSequence *int64
	var textCreatedAt, textExpiresAt *time.Time
	var textNonce, textCiphertext []byte
	var attachmentRevisionID, attachmentRevisionKind *string
	var attachmentServerSequence *int64
	var attachmentCreatedAt, attachmentExpiresAt *time.Time
	if err := row.Scan(
		&workspaceID, &aggregate.PasteID, &aggregate.RevisionID,
		&aggregate.ServerSequence, &aggregate.CreatedAt, &aggregate.Deleted,
		&textRevisionID, &textServerSequence, &textCreatedAt, &textExpiresAt,
		&textKeyID, &textNonce, &textCiphertext,
		&attachmentRevisionID, &attachmentRevisionKind,
		&attachmentServerSequence, &attachmentCreatedAt, &attachmentExpiresAt,
	); err != nil {
		return identity.PasteAggregate{}, err
	}
	if aggregate.Deleted {
		return aggregate, nil
	}
	if textRevisionID != nil {
		text := identity.TextRevision{
			WorkspaceID: workspaceID, PasteID: aggregate.PasteID,
			RevisionID: *textRevisionID, RevisionKind: identity.RevisionContent,
			ServerSequence: *textServerSequence, CreatedAt: *textCreatedAt,
			ExpiresAt: *textExpiresAt,
		}
		if textKeyID != nil {
			text.Envelope = secure.Envelope{KeyID: *textKeyID, Nonce: textNonce, Ciphertext: textCiphertext}
		}
		aggregate.TextExpiresAt = *textExpiresAt
		aggregate.TextRevision = &text
	}
	if attachmentRevisionID != nil {
		attachment := identity.TextRevision{
			WorkspaceID: workspaceID, PasteID: aggregate.PasteID,
			RevisionID: *attachmentRevisionID, RevisionKind: *attachmentRevisionKind,
			ServerSequence: *attachmentServerSequence, CreatedAt: *attachmentCreatedAt,
			ExpiresAt: *attachmentExpiresAt, Assets: make([]identity.ImageAsset, 0),
		}
		aggregate.AttachmentRevisionID = *attachmentRevisionID
		aggregate.AttachmentExpiresAt = *attachmentExpiresAt
		aggregate.AttachmentRevision = &attachment
	}
	return aggregate, nil
}

func (s *txStore) loadAggregateAssets(ctx context.Context, aggregates []identity.PasteAggregate) error {
	revisionIDs := make([]string, 0, len(aggregates))
	assetsByRevision := make(map[string][]identity.ImageAsset, len(aggregates))
	for index := range aggregates {
		if aggregates[index].AttachmentRevision == nil {
			continue
		}
		revisionID := aggregates[index].AttachmentRevisionID
		revisionIDs = append(revisionIDs, revisionID)
		assetsByRevision[revisionID] = make([]identity.ImageAsset, 0)
	}
	if len(revisionIDs) == 0 {
		return nil
	}
	rows, err := s.tx.Query(ctx, `
select workspace_id::text, paste_id::text, revision_id::text, asset_index,
       mime_type, width, height, byte_size, expires_at, storage_key,
       image_key_id, image_nonce
from paste_assets
where revision_id = any($1::uuid[])
order by revision_id, asset_index`, revisionIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var asset identity.ImageAsset
		if err := rows.Scan(
			&asset.WorkspaceID, &asset.PasteID, &asset.RevisionID, &asset.AssetIndex,
			&asset.MIMEType, &asset.Width, &asset.Height, &asset.ByteSize, &asset.ExpiresAt,
			&asset.StorageKey, &asset.Envelope.KeyID, &asset.Envelope.Nonce,
		); err != nil {
			return err
		}
		assetsByRevision[asset.RevisionID] = append(assetsByRevision[asset.RevisionID], asset)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range aggregates {
		if aggregates[index].AttachmentRevision == nil {
			continue
		}
		aggregates[index].AttachmentRevision.Assets = assetsByRevision[aggregates[index].AttachmentRevisionID]
	}
	return nil
}
