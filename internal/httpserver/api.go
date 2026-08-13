package httpserver

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

type apiServer struct {
	identity identityAPI
	proxies  []*net.IPNet
}

func NewApplicationHandler(readiness ReadinessFunc, service identityAPI, proxies []*net.IPNet) http.Handler {
	server := &apiServer{identity: service, proxies: proxies}
	mux := http.NewServeMux()
	registerHealth(mux, readiness)
	mux.HandleFunc("POST /v1/workspaces", server.createWorkspace)
	mux.HandleFunc("POST /v1/pairing-requests", server.createPairing)
	mux.HandleFunc("POST /v1/pairing-requests/lookup", server.lookupPairing)
	mux.HandleFunc("GET /v1/pairing-requests/{pairing_id}", server.getPairing)
	mux.HandleFunc("POST /v1/pairing-requests/{pairing_id}/approve", server.approvePairing)
	mux.HandleFunc("POST /v1/pairing-requests/{pairing_id}/claim", server.claimPairing)
	mux.HandleFunc("GET /v1/devices", server.listDevices)
	mux.HandleFunc("PATCH /v1/devices/{device_id}", server.renameDevice)
	mux.HandleFunc("DELETE /v1/devices/{device_id}", server.revokeDevice)
	mux.HandleFunc("POST /v1/recoveries", server.recover)
	mux.HandleFunc("POST /v1/pastes", server.createPaste)
	mux.HandleFunc("PATCH /v1/pastes/{paste_id}", server.updatePaste)
	mux.HandleFunc("DELETE /v1/pastes/{paste_id}", server.deletePaste)
	mux.HandleFunc("GET /v1/pastes", server.listPastes)
	mux.HandleFunc("GET /v1/sync", server.sync)
	return v1MethodGuard(mux)
}

func v1MethodGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1" && !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		methods, known := v1RouteMethods(r.URL.Path)
		if !known {
			writeError(w, identity.ErrNotFound)
			return
		}
		for _, method := range methods {
			if r.Method == method {
				next.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Allow", strings.Join(methods, ", "))
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"error": map[string]string{"code": "invalid_request"},
		})
	})
}

func v1RouteMethods(path string) ([]string, bool) {
	switch path {
	case "/v1/workspaces", "/v1/pairing-requests", "/v1/pairing-requests/lookup", "/v1/recoveries":
		return []string{http.MethodPost}, true
	case "/v1/devices":
		return []string{http.MethodGet}, true
	case "/v1/pastes":
		return []string{http.MethodGet, http.MethodPost}, true
	case "/v1/sync":
		return []string{http.MethodGet}, true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/v1/"), "/")
	if len(parts) == 2 && parts[0] == "pairing-requests" && parts[1] != "" {
		return []string{http.MethodGet}, true
	}
	if len(parts) == 3 && parts[0] == "pairing-requests" && parts[1] != "" {
		switch parts[2] {
		case "approve", "claim":
			return []string{http.MethodPost}, true
		}
	}
	if len(parts) == 2 && parts[0] == "devices" && parts[1] != "" {
		return []string{http.MethodPatch, http.MethodDelete}, true
	}
	if len(parts) == 2 && parts[0] == "pastes" && parts[1] != "" {
		return []string{http.MethodPatch, http.MethodDelete}, true
	}
	return nil, false
}

func (s *apiServer) createWorkspace(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, err := oneHeader(r, "Idempotency-Key")
	if err != nil {
		writeError(w, err)
		return
	}
	var input identity.CreateWorkspaceInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.CreateWorkspace(r.Context(), clientIP(r, s.proxies), idempotencyKey, input)
	writeResultOrError(w, result, err)
}

func (s *apiServer) createPairing(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, err := oneHeader(r, "Idempotency-Key")
	if err != nil {
		writeError(w, err)
		return
	}
	var input identity.CreatePairingInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.CreatePairing(r.Context(), clientIP(r, s.proxies), idempotencyKey, input)
	writeResultOrError(w, result, err)
}

func (s *apiServer) getPairing(w http.ResponseWriter, r *http.Request) {
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
	details, err := s.identity.PairingByID(r.Context(), principal, r.PathValue("pairing_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, details)
}

func (s *apiServer) lookupPairing(w http.ResponseWriter, r *http.Request) {
	principal, err := authenticate(r, s.identity)
	if err == nil {
		err = requireFull(principal)
	}
	var input struct {
		ShortCode string `json:"short_code"`
	}
	if err == nil {
		err = decodeJSON(w, r, &input)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	details, err := s.identity.PairingByShortCode(r.Context(), principal, input.ShortCode)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, details)
}

func (s *apiServer) approvePairing(w http.ResponseWriter, r *http.Request) {
	principal, err := authenticate(r, s.identity)
	if err == nil {
		err = requireFull(principal)
	}
	var idempotencyKey string
	if err == nil {
		idempotencyKey, err = oneHeader(r, "Idempotency-Key")
	}
	var input struct{}
	if err == nil {
		err = decodeJSON(w, r, &input)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.ApprovePairing(r.Context(), principal, r.PathValue("pairing_id"), idempotencyKey)
	writeResultOrError(w, result, err)
}

func (s *apiServer) claimPairing(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ClaimSecret string `json:"claim_secret"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.ClaimPairing(r.Context(), clientIP(r, s.proxies), r.PathValue("pairing_id"), input.ClaimSecret)
	writeResultOrError(w, result, err)
}

func (s *apiServer) listDevices(w http.ResponseWriter, r *http.Request) {
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
	devices, err := s.identity.ListDevices(r.Context(), principal)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Devices []identity.DeviceSummary `json:"devices"`
	}{Devices: devices})
}

func (s *apiServer) renameDevice(w http.ResponseWriter, r *http.Request) {
	principal, err := authenticate(r, s.identity)
	if err == nil {
		err = requireFull(principal)
	}
	var idempotencyKey string
	if err == nil {
		idempotencyKey, err = oneHeader(r, "Idempotency-Key")
	}
	var input identity.RenameInput
	if err == nil {
		err = decodeJSON(w, r, &input)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.RenameDevice(r.Context(), principal, r.PathValue("device_id"), idempotencyKey, input)
	writeResultOrError(w, result, err)
}

func (s *apiServer) revokeDevice(w http.ResponseWriter, r *http.Request) {
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
	result, err := s.identity.RevokeDevice(r.Context(), principal, r.PathValue("device_id"), idempotencyKey)
	writeResultOrError(w, result, err)
}

func (s *apiServer) recover(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, err := oneHeader(r, "Idempotency-Key")
	if err != nil {
		writeError(w, err)
		return
	}
	var input identity.RecoveryInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.identity.Recover(r.Context(), clientIP(r, s.proxies), idempotencyKey, input)
	writeResultOrError(w, result, err)
}

func oneHeader(r *http.Request, name string) (string, error) {
	values := r.Header.Values(name)
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", identity.ErrInvalid
	}
	return values[0], nil
}

func writeResultOrError(w http.ResponseWriter, result identity.Result, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	writeResult(w, result)
}

func writeError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, identity.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, identity.ErrUnauthorized):
		w.Header().Set("WWW-Authenticate", "Bearer")
		status, code = http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, identity.ErrForbidden):
		status, code = http.StatusForbidden, "forbidden"
	case errors.Is(err, identity.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, identity.ErrIdempotencyConflict):
		status, code = http.StatusConflict, "idempotency_conflict"
	case errors.Is(err, identity.ErrPairingPending):
		status, code = http.StatusConflict, "pairing_pending"
	case errors.Is(err, identity.ErrPairingApproved):
		status, code = http.StatusConflict, "pairing_already_approved"
	case errors.Is(err, identity.ErrPairingExpired):
		status, code = http.StatusGone, "pairing_expired"
	case errors.Is(err, identity.ErrInvalidClaim):
		status, code = http.StatusUnauthorized, "invalid_claim"
	case errors.Is(err, identity.ErrInvalidRecovery):
		status, code = http.StatusUnauthorized, "invalid_recovery"
	case errors.Is(err, identity.ErrRateLimited):
		status, code = http.StatusTooManyRequests, "rate_limited"
		if seconds, ok := identity.RetryAfterSeconds(err); ok {
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
		}
	case errors.Is(err, identity.ErrCursorExpired):
		status, code = http.StatusGone, "cursor_expired"
	case errors.Is(err, identity.ErrUnavailableContent):
		status, code = http.StatusServiceUnavailable, "unavailable_content"
	}
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code}})
}
