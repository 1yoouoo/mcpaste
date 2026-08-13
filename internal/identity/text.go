package identity

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/images"
	"github.com/1yoouoo/mcpaste/internal/secure"
)

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
			expiresAt := now.AddDate(1, 0, 0)
			envelope, err := s.keyring.Encrypt("paste-text", textObjectID(principal.WorkspaceID, pasteID, revisionID), []byte(input.Text))
			if err != nil {
				return nil, "", err
			}
			revision, err := tx.AppendTextRevision(ctx, principal.WorkspaceID, pasteID, revisionID, "content", "paste.created", envelope, now, expiresAt)
			if err != nil {
				return nil, "", err
			}
			return s.pasteResponse(revision, input.Text), principal.WorkspaceID, nil
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
			expiresAt := now.AddDate(1, 0, 0)
			envelope, err := s.keyring.Encrypt("paste-text", textObjectID(principal.WorkspaceID, pasteID, revisionID), []byte(input.Text))
			if err != nil {
				return nil, "", err
			}
			revision, err := tx.AppendTextRevision(ctx, principal.WorkspaceID, pasteID, revisionID, "content", "paste.revised", envelope, now, expiresAt)
			if err != nil {
				return nil, "", err
			}
			return s.pasteResponse(revision, input.Text), principal.WorkspaceID, nil
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
			expiresAt := now.AddDate(1, 0, 0)
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
	var revisions []TextRevision
	err := s.store.WithinTx(ctx, func(tx TxStore) error {
		var err error
		now := s.clock.Now()
		revisions, err = tx.ListPastes(ctx, principal.WorkspaceID, now.Add(-TextHistoryWindow), now)
		return err
	})
	if err != nil {
		return nil, err
	}
	result := make([]PasteResponse, 0, len(revisions))
	for _, revision := range revisions {
		if revision.RevisionKind == "image_bundle" {
			var assets []ImageAsset
			err := s.store.WithinTx(ctx, func(tx TxStore) error {
				var err error
				assets, err = tx.ListImageAssets(ctx, principal.WorkspaceID, revision.PasteID, revision.RevisionID)
				return err
			})
			if err != nil {
				return nil, err
			}
			revision.Assets = assets
			result = append(result, imageResponse(revision))
			continue
		}
		text, err := s.decryptText(principal.WorkspaceID, revision)
		if err != nil {
			return nil, err
		}
		result = append(result, s.pasteResponse(revision, text))
	}
	return result, nil
}

func (s *Service) Sync(ctx context.Context, principal Principal, after int64, limit int) (SyncResponse, error) {
	if principal.Scope != "full" {
		return SyncResponse{}, ErrForbidden
	}
	var syncResult SyncResult
	err := s.store.WithinTx(ctx, func(tx TxStore) error {
		var err error
		syncResult, err = tx.Sync(ctx, principal.WorkspaceID, after, limit, s.clock.Now())
		return err
	})
	if err != nil {
		return SyncResponse{}, err
	}
	response := SyncResponse{Cursor: syncResult.Cursor, HasMore: syncResult.HasMore, Events: make([]SyncEventResponse, 0, len(syncResult.Events))}
	for _, event := range syncResult.Events {
		item := SyncEventResponse{
			Sequence: event.Sequence, EventType: event.EventType, PasteID: event.PasteID,
			RevisionID: event.RevisionID, Kind: event.RevisionKind, ServerSequence: event.ServerSequence,
			CreatedAt: wireTime(event.CreatedAt), ExpiresAt: wireTime(event.ExpiresAt), Deleted: event.RevisionKind == "tombstone",
		}
		if event.RevisionKind == "image_bundle" {
			var assets []ImageAsset
			err := s.store.WithinTx(ctx, func(tx TxStore) error {
				var err error
				assets, err = tx.ListImageAssets(ctx, principal.WorkspaceID, event.PasteID, event.RevisionID)
				return err
			})
			if err != nil {
				return SyncResponse{}, err
			}
			item.Assets = imageAssetResponses(assets)
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
	result := make([]ImageAssetResponse, 0, len(assets))
	for _, asset := range assets {
		result = append(result, ImageAssetResponse{AssetIndex: asset.AssetIndex, MIMEType: asset.MIMEType, Width: asset.Width, Height: asset.Height, ByteSize: asset.ByteSize})
	}
	return result
}

func (s *Service) LatestPaste(ctx context.Context, principal Principal) (LatestPaste, error) {
	if principal.Scope != "connector" {
		return LatestPaste{}, ErrForbidden
	}
	var latest LatestPaste
	err := s.store.WithinTx(ctx, func(tx TxStore) error {
		var err error
		latest, err = tx.LatestPaste(ctx, principal.WorkspaceID, s.clock.Now())
		if errors.Is(err, ErrNotFound) {
			latest = LatestPaste{}
			return nil
		}
		if err != nil {
			return err
		}
		if !latest.Available {
			return nil
		}
		if len(latest.Images) == 0 {
			latest.Text, err = s.decryptEnvelope(principal.WorkspaceID, latest.PasteID, latest.RevisionID, latest.Envelope)
			if err != nil {
				return err
			}
		}
		if latest.RevisionID != "" && len(latest.Images) > 0 {
			if s.imageStore == nil {
				return ErrUnavailableContent
			}
			for index := range latest.Images {
				asset := latest.Images[index]
				latest.Images[index].Bytes, err = s.imageStore.Open(images.StoredAsset{StorageKey: asset.StorageKey, Envelope: asset.Envelope})
				if err != nil {
					return ErrUnavailableContent
				}
			}
		}
		return tx.TouchPaste(ctx, principal.WorkspaceID, latest.PasteID, s.clock.Now())
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
