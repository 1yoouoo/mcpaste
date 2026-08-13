package identity

import (
	"context"
	"errors"
)

func (s *Service) Snapshot(ctx context.Context, principal Principal) (SnapshotResponse, error) {
	if principal.Scope != "full" {
		return SnapshotResponse{}, ErrForbidden
	}
	var snapshot SnapshotResult
	err := s.store.WithinTx(ctx, func(tx TxStore) error {
		var err error
		snapshot, err = tx.Snapshot(ctx, principal.WorkspaceID, s.clock.Now())
		if err != nil {
			return err
		}
		for index := range snapshot.Revisions {
			if snapshot.Revisions[index].RevisionKind == "image_bundle" {
				snapshot.Revisions[index].Assets, err = tx.ListImageAssets(ctx, principal.WorkspaceID, snapshot.Revisions[index].PasteID, snapshot.Revisions[index].RevisionID)
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return SnapshotResponse{}, err
	}
	response := SnapshotResponse{Cursor: snapshot.Cursor, Pastes: make([]PasteResponse, 0, len(snapshot.Revisions))}
	for _, revision := range snapshot.Revisions {
		if revision.RevisionKind == "image_bundle" {
			response.Pastes = append(response.Pastes, imageResponse(revision))
			continue
		}
		text, err := s.decryptText(principal.WorkspaceID, revision)
		if errors.Is(err, ErrUnavailableContent) {
			continue
		}
		if err != nil {
			return SnapshotResponse{}, err
		}
		response.Pastes = append(response.Pastes, s.pasteResponse(revision, text))
	}
	return response, nil
}
