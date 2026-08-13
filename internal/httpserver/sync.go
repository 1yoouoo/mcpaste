package httpserver

import (
	"net/http"
	"strconv"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

const maxSyncLimit = 100

func (s *apiServer) sync(w http.ResponseWriter, r *http.Request) {
	err := requireEmptyBody(r)
	var principal identity.Principal
	if err == nil {
		principal, err = authenticate(r, s.identity)
	}
	if err == nil {
		err = requireFull(principal)
	}
	after, limit, parseErr := parseSyncQuery(r)
	if err == nil {
		err = parseErr
	}
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.Sync(r.Context(), principal, after, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func parseSyncQuery(r *http.Request) (int64, int, error) {
	query := r.URL.Query()
	for name := range query {
		if name != "after" && name != "limit" {
			return 0, 0, identity.ErrInvalid
		}
		if len(query[name]) != 1 {
			return 0, 0, identity.ErrInvalid
		}
	}
	after := int64(0)
	if value := query.Get("after"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 0 {
			return 0, 0, identity.ErrInvalid
		}
		after = parsed
	} else if _, present := query["after"]; present {
		return 0, 0, identity.ErrInvalid
	}
	limit := maxSyncLimit
	if value := query.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			return 0, 0, identity.ErrInvalid
		}
		if parsed < limit {
			limit = parsed
		}
	} else if _, present := query["limit"]; present {
		return 0, 0, identity.ErrInvalid
	}
	return after, limit, nil
}
