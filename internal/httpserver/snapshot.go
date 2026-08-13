package httpserver

import (
	"context"
	"net/http"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

type snapshotAPI interface {
	Snapshot(context.Context, identity.Principal) (identity.SnapshotResponse, error)
}

func (s *apiServer) snapshot(w http.ResponseWriter, r *http.Request) {
	principal, err := authenticate(r, s.identity)
	if err == nil {
		err = requireFull(principal)
	}
	service, ok := s.identity.(snapshotAPI)
	if err == nil && !ok {
		err = identity.ErrUnavailableContent
	}
	if err == nil {
		err = requireEmptyBody(r)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	response, err := service.Snapshot(r.Context(), principal)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}
