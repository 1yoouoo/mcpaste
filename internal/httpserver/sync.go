package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

const maxSyncLimit = 100
const ssePollInterval = 100 * time.Millisecond
const sseHeartbeatInterval = 15 * time.Second

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

func (s *apiServer) events(w http.ResponseWriter, r *http.Request) {
	err := requireEmptyBody(r)
	var principal identity.Principal
	if err == nil {
		principal, err = authenticate(r, s.identity)
	}
	if err == nil {
		err = requireFull(principal)
	}
	cursor, cursorErr := parseEventCursor(r)
	if err == nil {
		err = cursorErr
	}
	if err != nil {
		writeError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, identity.ErrInvalid)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	poll := time.NewTicker(ssePollInterval)
	defer poll.Stop()
	heartbeat := time.NewTicker(sseHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		result, err := s.identity.Sync(r.Context(), principal, cursor, maxSyncLimit)
		if err != nil {
			return
		}
		for _, event := range result.Events {
			if err := writeInvalidation(w, flusher, event.Sequence); err != nil {
				return
			}
			cursor = event.Sequence
		}
		if result.HasMore {
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func parseEventCursor(r *http.Request) (int64, error) {
	query := r.URL.Query()
	for name := range query {
		if name != "after" {
			return 0, identity.ErrInvalid
		}
		if len(query[name]) != 1 {
			return 0, identity.ErrInvalid
		}
	}
	if values, present := query["after"]; present {
		if values[0] == "" {
			return 0, identity.ErrInvalid
		}
		return parseNonNegativeSequence(values[0])
	}
	values := r.Header.Values("Last-Event-ID")
	if len(values) > 1 || (len(values) == 1 && values[0] == "") {
		return 0, identity.ErrInvalid
	}
	if len(values) == 1 {
		return parseNonNegativeSequence(values[0])
	}
	return 0, nil
}

func parseNonNegativeSequence(value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, identity.ErrInvalid
	}
	return parsed, nil
}

func writeInvalidation(w http.ResponseWriter, flusher http.Flusher, sequence int64) error {
	payload, err := json.Marshal(struct {
		Sequence int64 `json:"sequence"`
	}{Sequence: sequence})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: invalidation\nid: %d\ndata: %s\n\n", sequence, payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
