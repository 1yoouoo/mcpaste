package identity

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/images"
	"github.com/1yoouoo/mcpaste/internal/secure"
)

func retentionExpiry(now time.Time) time.Time {
	expiresAt := now.AddDate(1, 0, 0)
	if now.Month() == time.February && now.Day() == 29 && expiresAt.Month() == time.March && expiresAt.Day() == 1 {
		return expiresAt.AddDate(0, 0, -1)
	}
	return expiresAt
}

func (s *Service) CreatePaste(ctx context.Context, principal Principal, idempotencyKey string, input CreatePasteInput) (Result, error) {
	if principal.Scope != "full" {
		return Result{}, ErrForbidden
	}
	pasteID, err := secure.NewUUID(s.random)
	if err != nil {
		return Result{}, err
	}
	revisionID, err := secure.NewUUID(s.random)
	if err != nil {
		return Result{}, err
	}
	canonical, _ := json.Marshal(input)
	return s.mutate(ctx, principal.WorkspaceID, "paste.create", idempotencyKey, principal.WorkspaceID, canonical, 201,
		func(tx TxStore, now time.Time) (any, string, error) {
			if err := tx.InsertPaste(ctx, principal.WorkspaceID, pasteID, now); err != nil {
				return nil, "", err
			}
			if err := tx.SetPasteKind(ctx, principal.WorkspaceID, pasteID, "text"); err != nil {
				return nil, "", err
			}
			expiresAt := retentionExpiry(now)
			envelope, err := s.keyring.Encrypt("paste-text", textObjectID(principal.WorkspaceID, pasteID, revisionID), []byte(input.Text))
			if err != nil {
				return nil, "", err
			}
			if _, err := tx.AppendTextRevision(ctx, principal.WorkspaceID, pasteID, revisionID, "content", "paste.created", envelope, now, expiresAt); err != nil {
				return nil, "", err
			}
			aggregate, err := tx.PasteAggregate(ctx, principal.WorkspaceID, pasteID, now)
			if err != nil {
				return nil, "", err
			}
			response, err := s.aggregateResponse(ctx, aggregate)
			if err != nil {
				return nil, "", err
			}
			return response, principal.WorkspaceID, nil
		})
}

func (s *Service) UpdatePaste(ctx context.Context, principal Principal, pasteID, idempotencyKey string, input UpdatePasteInput) (Result, error) {
	if principal.Scope != "full" {
		return Result{}, ErrForbidden
	}
	if !secure.ValidUUID(pasteID) {
		return Result{}, ErrInvalid
	}
	revisionID, err := secure.NewUUID(s.random)
	if err != nil {
		return Result{}, err
	}
	canonical, _ := json.Marshal(input)
	return s.mutate(ctx, principal.WorkspaceID, "paste.update:"+pasteID, idempotencyKey, principal.WorkspaceID, canonical, 200,
		func(tx TxStore, now time.Time) (any, string, error) {
			if err := tx.SetPasteKind(ctx, principal.WorkspaceID, pasteID, "text"); err != nil {
				return nil, "", err
			}
			expiresAt := retentionExpiry(now)
			envelope, err := s.keyring.Encrypt("paste-text", textObjectID(principal.WorkspaceID, pasteID, revisionID), []byte(input.Text))
			if err != nil {
				return nil, "", err
			}
			if _, err := tx.AppendTextRevision(ctx, principal.WorkspaceID, pasteID, revisionID, "content", "paste.revised", envelope, now, expiresAt); err != nil {
				return nil, "", err
			}
			aggregate, err := tx.PasteAggregate(ctx, principal.WorkspaceID, pasteID, now)
			if err != nil {
				return nil, "", err
			}
			response, err := s.aggregateResponse(ctx, aggregate)
			if err != nil {
				return nil, "", err
			}
			return response, principal.WorkspaceID, nil
		})
}

func (s *Service) DeletePaste(ctx context.Context, principal Principal, pasteID, idempotencyKey string) (Result, error) {
	if principal.Scope != "full" {
		return Result{}, ErrForbidden
	}
	if !secure.ValidUUID(pasteID) {
		return Result{}, ErrInvalid
	}
	revisionID, err := secure.NewUUID(s.random)
	if err != nil {
		return Result{}, err
	}
	result, err := s.mutate(ctx, principal.WorkspaceID, "paste.delete:"+pasteID, idempotencyKey, principal.WorkspaceID, []byte("{}"), 204,
		func(tx TxStore, now time.Time) (any, string, error) {
			expiresAt := retentionExpiry(now)
			if _, err := tx.AppendTextRevision(ctx, principal.WorkspaceID, pasteID, revisionID, "tombstone", "paste.deleted", secure.Envelope{}, now, expiresAt); err != nil {
				return nil, "", err
			}
			return nil, principal.WorkspaceID, nil
		})
	if err == nil && s.imageStore != nil {
		_ = s.imageStore.RemovePaste(principal.WorkspaceID, pasteID)
	}
	return result, err
}

func (s *Service) ListPastes(ctx context.Context, principal Principal) ([]PasteResponse, error) {
	if principal.Scope != "full" {
		return nil, ErrForbidden
	}
	var aggregates []PasteAggregate
	now := s.clock.Now()
	err := s.store.WithinTx(ctx, func(tx TxStore) error {
		var err error
		aggregates, err = tx.ListPasteAggregates(ctx, principal.WorkspaceID, now.Add(-TextHistoryWindow), now)
		return err
	})
	if err != nil {
		return nil, err
	}
	result := make([]PasteResponse, 0, len(aggregates))
	for _, aggregate := range aggregates {
		response, err := s.aggregateResponse(ctx, aggregate)
		if err != nil {
			return nil, err
		}
		result = append(result, response)
	}
	return result, nil
}

func (s *Service) Sync(ctx context.Context, principal Principal, after int64, limit int) (SyncResponse, error) {
	if principal.Scope != "full" {
		return SyncResponse{}, ErrForbidden
	}
	var syncResult SyncResult
	var eventAssets [][]ImageAsset
	err := s.store.WithinTx(ctx, func(tx TxStore) error {
		var err error
		syncResult, err = tx.Sync(ctx, principal.WorkspaceID, after, limit, s.clock.Now())
		if err != nil {
			return err
		}
		eventAssets = make([][]ImageAsset, len(syncResult.Events))
		for index, event := range syncResult.Events {
			if event.RevisionKind != RevisionAttachmentBundle && event.RevisionKind != RevisionImageBundle {
				continue
			}
			eventAssets[index], err = tx.ListImageAssets(
				ctx, principal.WorkspaceID, event.PasteID, event.RevisionID,
			)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return SyncResponse{}, err
	}
	response := SyncResponse{Cursor: syncResult.Cursor, HasMore: syncResult.HasMore, Events: make([]SyncEventResponse, 0, len(syncResult.Events))}
	for index, event := range syncResult.Events {
		item := SyncEventResponse{
			Sequence: event.Sequence, EventType: event.EventType, PasteID: event.PasteID,
			RevisionID: event.RevisionID, Kind: event.RevisionKind, ServerSequence: event.ServerSequence,
			CreatedAt: wireTime(event.CreatedAt), ExpiresAt: wireTime(event.ExpiresAt), Deleted: event.RevisionKind == "tombstone",
		}
		if event.RevisionKind == RevisionAttachmentBundle || event.RevisionKind == RevisionImageBundle {
			assetResponses := imageAssetResponses(eventAssets[index])
			item.Assets = &assetResponses
		}
		if event.RevisionKind == "content" {
			text, err := s.decryptText(principal.WorkspaceID, TextRevision{
				WorkspaceID: principal.WorkspaceID, PasteID: event.PasteID, RevisionID: event.RevisionID,
				RevisionKind: event.RevisionKind, ServerSequence: event.ServerSequence, CreatedAt: event.CreatedAt,
				ExpiresAt: event.ExpiresAt, Envelope: event.Envelope,
			})
			if err != nil {
				return SyncResponse{}, err
			}
			item.Text = &text
		}
		response.Events = append(response.Events, item)
	}
	return response, nil
}

func imageAssetResponses(assets []ImageAsset) []ImageAssetResponse {
	return attachmentResponses(assets)
}

func (s *Service) LatestPaste(ctx context.Context, principal Principal) (LatestPaste, error) {
	if principal.Scope != "connector" {
		return LatestPaste{}, ErrForbidden
	}
	now := s.clock.Now()
	var aggregate PasteAggregate
	found := false
	err := s.store.WithinTx(ctx, func(tx TxStore) error {
		var err error
		aggregate, err = tx.LatestPasteAggregate(ctx, principal.WorkspaceID, now)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil || !found {
		return LatestPaste{}, err
	}
	latest := LatestPaste{
		Available:      true,
		PasteID:        aggregate.PasteID,
		RevisionID:     aggregate.RevisionID,
		ServerSequence: aggregate.ServerSequence,
		CreatedAt:      aggregate.CreatedAt,
	}
	if aggregate.TextRevision != nil {
		if s.keyring == nil {
			return LatestPaste{}, ErrUnavailableContent
		}
		latest.Text, err = s.decryptText(principal.WorkspaceID, *aggregate.TextRevision)
		if err != nil {
			return LatestPaste{}, err
		}
		latest.ExpiresAt = aggregate.TextExpiresAt
	}
	if aggregate.AttachmentRevision != nil {
		latest.Images = append([]ImageAsset(nil), aggregate.AttachmentRevision.Assets...)
		if aggregate.TextRevision == nil {
			latest.ExpiresAt = aggregate.AttachmentExpiresAt
		}
		if len(latest.Images) > 0 && s.imageStore == nil {
			return LatestPaste{}, ErrUnavailableContent
		}
		for index := range latest.Images {
			asset := latest.Images[index]
			latest.Images[index].Bytes, err = s.imageStore.Open(images.StoredAsset{StorageKey: asset.StorageKey, Envelope: asset.Envelope})
			if err != nil {
				return LatestPaste{}, ErrUnavailableContent
			}
		}
	}
	err = s.store.WithinTx(ctx, func(tx TxStore) error {
		return tx.TouchPaste(ctx, principal.WorkspaceID, latest.PasteID, now)
	})
	return latest, err
}

func (s *Service) PurgeText(ctx context.Context) (CleanupResult, error) {
	revisions, pastes, err := s.store.PurgeText(ctx, s.clock.Now())
	return CleanupResult{TextRevisionRows: revisions, TextPasteRows: pastes}, err
}

func (s *Service) decryptText(workspaceID string, revision TextRevision) (string, error) {
	return s.decryptEnvelope(workspaceID, revision.PasteID, revision.RevisionID, revision.Envelope)
}

func (s *Service) decryptEnvelope(workspaceID, pasteID, revisionID string, envelope secure.Envelope) (string, error) {
	plain, err := s.keyring.Decrypt("paste-text", textObjectID(workspaceID, pasteID, revisionID), envelope)
	if err != nil {
		return "", ErrUnavailableContent
	}
	return string(plain), nil
}

func (s *Service) pasteResponse(revision TextRevision, text string) PasteResponse {
	return PasteResponse{
		PasteID: revision.PasteID, RevisionID: revision.RevisionID, Kind: revision.RevisionKind,
		ServerSequence: revision.ServerSequence, CreatedAt: wireTime(revision.CreatedAt),
		ExpiresAt: wireTime(revision.ExpiresAt), Deleted: revision.RevisionKind == "tombstone", Text: &text,
	}
}

func textObjectID(workspaceID, pasteID, revisionID string) string {
	return workspaceID + ":" + pasteID + ":" + revisionID
}
