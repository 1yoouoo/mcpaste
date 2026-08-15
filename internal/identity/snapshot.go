package identity

import (
	"context"
	"errors"
)

func (s *Service) Snapshot(ctx context.Context, principal Principal) (SnapshotResponse, error) {
	if principal.Scope != "full" {
		return SnapshotResponse{}, ErrForbidden
	}
	var cursor int64
	var aggregates []PasteAggregate
	err := s.store.WithinTx(ctx, func(tx TxStore) error {
		var err error
		cursor, aggregates, err = tx.SnapshotAggregates(ctx, principal.WorkspaceID, s.clock.Now())
		return err
	})
	if err != nil {
		return SnapshotResponse{}, err
	}
	response := SnapshotResponse{Cursor: cursor, Pastes: make([]PasteResponse, 0, len(aggregates))}
	for _, aggregate := range aggregates {
		paste, err := s.aggregateResponse(ctx, aggregate)
		if errors.Is(err, ErrUnavailableContent) {
			continue
		}
		if err != nil {
			return SnapshotResponse{}, err
		}
		response.Pastes = append(response.Pastes, paste)
	}
	return response, nil
}
