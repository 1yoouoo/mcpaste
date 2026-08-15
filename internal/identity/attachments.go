package identity

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/images"
	"github.com/1yoouoo/mcpaste/internal/secure"
)

type ReplaceAttachmentsInput struct {
	Assets []images.AssetInput
}

func (s *Service) ReplaceAttachments(
	ctx context.Context,
	principal Principal,
	pasteID, idempotencyKey string,
	input ReplaceAttachmentsInput,
) (Result, error) {
	if principal.Scope != "full" {
		return Result{}, ErrForbidden
	}
	if !secure.ValidUUID(pasteID) {
		return Result{}, ErrInvalid
	}
	if err := images.ValidateAttachmentBundle(input.Assets); err != nil {
		return Result{}, ErrInvalid
	}
	if s.imageStore == nil || s.keyring == nil {
		return Result{}, ErrUnavailableContent
	}
	canonical, err := json.Marshal(imageCanonicalInput(CreateImagePasteInput{Assets: input.Assets}))
	if err != nil {
		return Result{}, err
	}
	operation := "paste.attachments.replace:" + pasteID
	release, err := s.idempotencyGate.acquire(ctx, principal.WorkspaceID, operation, idempotencyKey)
	if err != nil {
		return Result{}, err
	}
	defer release()
	if replay, found, err := s.preflight(
		ctx, principal.WorkspaceID, operation, idempotencyKey, principal.WorkspaceID, canonical,
	); err != nil || found {
		return replay, err
	}

	revisionID, err := secure.NewUUID(s.random)
	if err != nil {
		return Result{}, err
	}

	writeAttempted := false
	removePreparedRevision := func(primaryErr error) error {
		if !writeAttempted {
			return primaryErr
		}
		cleanupErr := s.imageStore.RemoveTree(principal.WorkspaceID, pasteID, revisionID)
		if cleanupErr == nil {
			return primaryErr
		}
		if primaryErr == nil {
			return cleanupErr
		}
		return errors.Join(primaryErr, cleanupErr)
	}
	assets := make([]ImageAsset, 0, len(input.Assets))
	for index, inputAsset := range input.Assets {
		writeAttempted = true
		asset, err := s.imageStore.Put(principal.WorkspaceID, pasteID, revisionID, index, inputAsset.Bytes)
		if err != nil {
			return Result{}, removePreparedRevision(ErrUnavailableContent)
		}
		assets = append(assets, ImageAsset{
			WorkspaceID: principal.WorkspaceID,
			PasteID:     pasteID,
			RevisionID:  revisionID,
			AssetIndex:  index,
			MIMEType:    inputAsset.MIMEType,
			Width:       inputAsset.Width,
			Height:      inputAsset.Height,
			ByteSize:    int64(len(inputAsset.Bytes)),
			StorageKey:  asset.StorageKey,
			Envelope:    asset.Envelope,
		})
	}

	callbackCompleted := false
	result, mutateErr := s.mutate(
		ctx,
		principal.WorkspaceID,
		operation,
		idempotencyKey,
		principal.WorkspaceID,
		canonical,
		200,
		func(tx TxStore, now time.Time) (any, string, error) {
			expiresAt := now.Add(ImageLifetime)
			for index := range assets {
				assets[index].ExpiresAt = expiresAt
			}
			if _, err := tx.AppendAttachmentRevision(
				ctx, principal.WorkspaceID, pasteID, revisionID,
				"paste.revised", assets, now, expiresAt,
			); err != nil {
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
			callbackCompleted = true
			return response, principal.WorkspaceID, nil
		},
	)
	if mutateErr == nil {
		if !callbackCompleted {
			if cleanupErr := removePreparedRevision(nil); cleanupErr != nil {
				return result, cleanupErr
			}
		}
		return result, nil
	}
	if !callbackCompleted {
		return result, removePreparedRevision(mutateErr)
	}

	replay, found, verificationErr := s.preflight(
		ctx, principal.WorkspaceID, operation, idempotencyKey, principal.WorkspaceID, canonical,
	)
	if found {
		return replay, verificationErr
	}
	if verificationErr != nil {
		return Result{}, errors.Join(mutateErr, verificationErr)
	}
	return result, removePreparedRevision(mutateErr)
}

func (s *Service) AttachmentAsset(
	ctx context.Context,
	principal Principal,
	pasteID string,
	assetIndex int,
) (ImageAsset, []byte, error) {
	if principal.Scope != "full" {
		return ImageAsset{}, nil, ErrForbidden
	}
	if !secure.ValidUUID(pasteID) || assetIndex < 0 || assetIndex >= images.MaxBundleItems {
		return ImageAsset{}, nil, ErrInvalid
	}
	if s.imageStore == nil {
		return ImageAsset{}, nil, ErrUnavailableContent
	}

	var asset ImageAsset
	now := s.clock.Now()
	err := s.store.WithinTx(ctx, func(tx TxStore) error {
		var err error
		asset, err = tx.CurrentAttachmentAsset(ctx, principal.WorkspaceID, pasteID, assetIndex, now)
		return err
	})
	if err != nil {
		return ImageAsset{}, nil, err
	}
	content, err := s.imageStore.Open(images.StoredAsset{StorageKey: asset.StorageKey, Envelope: asset.Envelope})
	if err != nil {
		return ImageAsset{}, nil, ErrUnavailableContent
	}
	return asset, content, nil
}

func (s *Service) aggregateResponse(ctx context.Context, aggregate PasteAggregate) (PasteResponse, error) {
	_ = ctx
	response := PasteResponse{
		PasteID:        aggregate.PasteID,
		RevisionID:     aggregate.RevisionID,
		ServerSequence: aggregate.ServerSequence,
		CreatedAt:      wireTime(aggregate.CreatedAt),
		Deleted:        aggregate.Deleted,
	}
	if aggregate.Deleted {
		response.Kind = RevisionTombstone
		return response, nil
	}

	if aggregate.TextRevision != nil {
		if s.keyring == nil {
			return PasteResponse{}, ErrUnavailableContent
		}
		text, err := s.decryptText(aggregate.TextRevision.WorkspaceID, *aggregate.TextRevision)
		if err != nil {
			return PasteResponse{}, err
		}
		response.Kind = RevisionContent
		response.ExpiresAt = wireTime(aggregate.TextExpiresAt)
		response.Text = &text
	}
	if aggregate.AttachmentRevision != nil {
		response.AttachmentRevisionID = aggregate.AttachmentRevisionID
		response.Assets = attachmentResponses(aggregate.AttachmentRevision.Assets)
		if aggregate.TextRevision == nil {
			response.Kind = aggregate.AttachmentRevision.RevisionKind
			response.ExpiresAt = wireTime(aggregate.AttachmentExpiresAt)
		}
	}
	return response, nil
}

func attachmentResponses(assets []ImageAsset) []ImageAssetResponse {
	result := make([]ImageAssetResponse, 0, len(assets))
	for _, asset := range assets {
		result = append(result, ImageAssetResponse{
			AssetIndex: asset.AssetIndex,
			MIMEType:   asset.MIMEType,
			Width:      asset.Width,
			Height:     asset.Height,
			ByteSize:   asset.ByteSize,
			ExpiresAt:  wireTime(asset.ExpiresAt),
		})
	}
	return result
}
