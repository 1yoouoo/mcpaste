package httpserver

import (
	"net/http"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

func (s *apiServer) createPaste(w http.ResponseWriter, r *http.Request) {
	principal, err := authenticate(r, s.identity)
	if err == nil {
		err = requireFull(principal)
	}
	var idempotencyKey string
	if err == nil {
		idempotencyKey, err = oneHeader(r, "Idempotency-Key")
	}
	var input identity.CreatePasteInput
	if err == nil {
		err = decodeJSON(w, r, &input)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.CreatePaste(r.Context(), principal, idempotencyKey, input)
	writeResultOrError(w, result, err)
}

func (s *apiServer) updatePaste(w http.ResponseWriter, r *http.Request) {
	principal, err := authenticate(r, s.identity)
	if err == nil {
		err = requireFull(principal)
	}
	var idempotencyKey string
	if err == nil {
		idempotencyKey, err = oneHeader(r, "Idempotency-Key")
	}
	var input identity.UpdatePasteInput
	if err == nil {
		err = decodeJSON(w, r, &input)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.UpdatePaste(r.Context(), principal, r.PathValue("paste_id"), idempotencyKey, input)
	writeResultOrError(w, result, err)
}

func (s *apiServer) deletePaste(w http.ResponseWriter, r *http.Request) {
	principal, err := authenticate(r, s.identity)
	if err == nil {
		err = requireFull(principal)
	}
	if err == nil {
		err = requireEmptyBody(r)
	}
	var idempotencyKey string
	if err == nil {
		idempotencyKey, err = oneHeader(r, "Idempotency-Key")
	}
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.DeletePaste(r.Context(), principal, r.PathValue("paste_id"), idempotencyKey)
	writeResultOrError(w, result, err)
}

func (s *apiServer) listPastes(w http.ResponseWriter, r *http.Request) {
	err := requireEmptyBody(r)
	var principal identity.Principal
	if err == nil {
		principal, err = authenticate(r, s.identity)
	}
	if err == nil {
		err = requireFull(principal)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	pastes, err := s.identity.ListPastes(r.Context(), principal)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Pastes []identity.PasteResponse `json:"pastes"`
	}{Pastes: pastes})
}
