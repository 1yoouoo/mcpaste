package identity

import (
	"context"
	"encoding/json"
	"time"

	"github.com/1yoouoo/mcpaste/internal/images"
	"github.com/1yoouoo/mcpaste/internal/secure"
)

type CreateImagePasteInput struct{ Assets []images.AssetInput }

func (s *Service) CreateImagePaste(ctx context.Context, principal Principal, idempotencyKey string, input CreateImagePasteInput) (Result, error) {
	return s.mutateImage(ctx, principal, "", idempotencyKey, input)
}

func (s *Service) UpdateImagePaste(ctx context.Context, principal Principal, pasteID, idempotencyKey string, input CreateImagePasteInput) (Result, error) {
	return s.mutateImage(ctx, principal, pasteID, idempotencyKey, input)
}

func (s *Service) mutateImage(ctx context.Context, principal Principal, pasteID, idempotencyKey string, input CreateImagePasteInput) (Result, error) {
	if principal.Scope != "full" {
		return Result{}, ErrForbidden
	}
	if s.imageStore == nil || s.keyring == nil {
		return Result{}, ErrUnavailableContent
	}
	isUpdate := pasteID != ""
	if isUpdate && !secure.ValidUUID(pasteID) {
		return Result{}, ErrInvalid
	}
	if err := images.ValidateBundle(input.Assets); err != nil {
		return Result{}, ErrInvalid
	}
	if pasteID == "" {
		var err error
		pasteID, err = secure.NewUUID(s.random)
		if err != nil {
			return Result{}, err
		}
	}
	revisionID, err := secure.NewUUID(s.random)
	if err != nil {
		return Result{}, err
	}
	canonical, _ := json.Marshal(imageCanonicalInput(input))
	operation := "paste.image.create"
	status := 201
	eventType := "paste.created"
	if isUpdate {
		operation = "paste.image.update:" + pasteID
		status = 200
		eventType = "paste.revised"
	}
	stored := make([]images.StoredAsset, 0, len(input.Assets))
	result, mutateErr := s.mutate(ctx, principal.WorkspaceID, operation, idempotencyKey, principal.WorkspaceID, canonical, status, func(tx TxStore, now time.Time) (any, string, error) {
		if eventType == "paste.created" {
			if err := tx.InsertPaste(ctx, principal.WorkspaceID, pasteID, now); err != nil {
				return nil, "", err
			}
		}
		if err := tx.SetPasteKind(ctx, principal.WorkspaceID, pasteID, "image_bundle"); err != nil {
			return nil, "", err
		}
		expiresAt := now.Add(ImageLifetime)
		assets := make([]ImageAsset, 0, len(input.Assets))
		for index, inputAsset := range input.Assets {
			asset, err := s.imageStore.Put(principal.WorkspaceID, pasteID, revisionID, index, inputAsset.Bytes)
			if err != nil {
				return nil, "", ErrInvalid
			}
			stored = append(stored, asset)
			assets = append(assets, ImageAsset{AssetIndex: index, MIMEType: inputAsset.MIMEType, Width: inputAsset.Width, Height: inputAsset.Height, ByteSize: int64(len(inputAsset.Bytes)), StorageKey: asset.StorageKey, Envelope: asset.Envelope})
		}
		revision, err := tx.AppendImageRevision(ctx, principal.WorkspaceID, pasteID, revisionID, eventType, assets, now, expiresAt)
		if err != nil {
			return nil, "", err
		}
		return imageResponse(revision), principal.WorkspaceID, nil
	})
	if mutateErr != nil {
		for _, asset := range stored {
			_ = s.imageStore.Remove(asset)
		}
	}
	return result, mutateErr
}

func (s *Service) ImageAsset(ctx context.Context, principal Principal, pasteID string, index int) (ImageAsset, []byte, error) {
	if principal.Scope != "full" || !secure.ValidUUID(pasteID) || index < 0 {
		return ImageAsset{}, nil, ErrForbidden
	}
	if s.imageStore == nil {
		return ImageAsset{}, nil, ErrUnavailableContent
	}
	var asset ImageAsset
	err := s.store.WithinTx(ctx, func(tx TxStore) error {
		latest, err := tx.LatestPaste(ctx, principal.WorkspaceID, s.clock.Now())
		if err != nil {
			return err
		}
		if latest.PasteID != pasteID {
			return ErrNotFound
		}
		assets, err := tx.ListImageAssets(ctx, principal.WorkspaceID, pasteID, latest.RevisionID)
		if err != nil {
			return err
		}
		for _, candidate := range assets {
			if candidate.AssetIndex == index {
				asset = candidate
				break
			}
		}
		if asset.StorageKey == "" {
			return ErrNotFound
		}
		if !asset.ExpiresAt.After(s.clock.Now()) {
			return ErrUnavailableContent
		}
		return nil
	})
	if err != nil {
		return ImageAsset{}, nil, err
	}
	bytes, err := s.imageStore.Open(images.StoredAsset{StorageKey: asset.StorageKey, Envelope: asset.Envelope})
	if err != nil {
		return ImageAsset{}, nil, ErrUnavailableContent
	}
	return asset, bytes, nil
}

func imageCanonicalInput(input CreateImagePasteInput) any {
	items := make([]map[string]any, 0, len(input.Assets))
	for _, asset := range input.Assets {
		items = append(items, map[string]any{"mime_type": asset.MIMEType, "width": asset.Width, "height": asset.Height, "byte_size": len(asset.Bytes)})
	}
	return map[string]any{"assets": items}
}

func imageResponse(revision TextRevision) PasteResponse {
	assets := make([]ImageAssetResponse, 0, len(revision.Assets))
	for _, asset := range revision.Assets {
		assets = append(assets, ImageAssetResponse{AssetIndex: asset.AssetIndex, MIMEType: asset.MIMEType, Width: asset.Width, Height: asset.Height, ByteSize: asset.ByteSize})
	}
	return PasteResponse{PasteID: revision.PasteID, RevisionID: revision.RevisionID, Kind: "image_bundle", ServerSequence: revision.ServerSequence, CreatedAt: wireTime(revision.CreatedAt), ExpiresAt: wireTime(revision.ExpiresAt), Assets: assets}
}
