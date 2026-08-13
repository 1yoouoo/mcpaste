package httpserver

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

type fakeIdentityAPI struct {
	createCalls int
}

func (f *fakeIdentityAPI) Authenticate(_ context.Context, token string) (identity.Principal, error) {
	if token == "connector-runtime-marker" {
		return identity.Principal{WorkspaceID: "00000000-0000-4000-8000-000000000001", DeviceID: "00000000-0000-4000-8000-000000000002", Scope: "connector"}, nil
	}
	return identity.Principal{}, identity.ErrUnauthorized
}

func (f *fakeIdentityAPI) CreateWorkspace(_ context.Context, _, _ string, _ identity.CreateWorkspaceInput) (identity.Result, error) {
	f.createCalls++
	return identity.Result{Status: 201, Body: []byte("{\"workspace_id\":\"runtime-marker\"}\n")}, nil
}

func (f *fakeIdentityAPI) CreatePairing(context.Context, string, string, identity.CreatePairingInput) (identity.Result, error) {
	return identity.Result{}, identity.ErrNotFound
}
func (f *fakeIdentityAPI) PairingByID(context.Context, identity.Principal, string) (identity.PairingDetails, error) {
	return identity.PairingDetails{}, identity.ErrNotFound
}
func (f *fakeIdentityAPI) PairingByShortCode(context.Context, identity.Principal, string) (identity.PairingDetails, error) {
	return identity.PairingDetails{}, identity.ErrNotFound
}
func (f *fakeIdentityAPI) ApprovePairing(context.Context, identity.Principal, string, string) (identity.Result, error) {
	return identity.Result{}, identity.ErrNotFound
}
func (f *fakeIdentityAPI) ClaimPairing(context.Context, string, string, string) (identity.Result, error) {
	return identity.Result{}, identity.ErrNotFound
}
func (f *fakeIdentityAPI) ListDevices(context.Context, identity.Principal) ([]identity.DeviceSummary, error) {
	return nil, identity.ErrForbidden
}
func (f *fakeIdentityAPI) RenameDevice(context.Context, identity.Principal, string, string, identity.RenameInput) (identity.Result, error) {
	return identity.Result{}, identity.ErrForbidden
}
func (f *fakeIdentityAPI) RevokeDevice(context.Context, identity.Principal, string, string) (identity.Result, error) {
	return identity.Result{}, identity.ErrForbidden
}
func (f *fakeIdentityAPI) Recover(context.Context, string, string, identity.RecoveryInput) (identity.Result, error) {
	return identity.Result{}, identity.ErrInvalidRecovery
}
func (f *fakeIdentityAPI) CreatePaste(context.Context, identity.Principal, string, identity.CreatePasteInput) (identity.Result, error) {
	return identity.Result{}, identity.ErrForbidden
}
func (f *fakeIdentityAPI) UpdatePaste(context.Context, identity.Principal, string, string, identity.UpdatePasteInput) (identity.Result, error) {
	return identity.Result{}, identity.ErrForbidden
}
func (f *fakeIdentityAPI) DeletePaste(context.Context, identity.Principal, string, string) (identity.Result, error) {
	return identity.Result{}, identity.ErrForbidden
}
func (f *fakeIdentityAPI) ListPastes(context.Context, identity.Principal) ([]identity.PasteResponse, error) {
	return nil, identity.ErrForbidden
}
func (f *fakeIdentityAPI) Sync(context.Context, identity.Principal, int64, int) (identity.SyncResponse, error) {
	return identity.SyncResponse{}, identity.ErrForbidden
}

func TestWorkspaceCreateUsesStrictJSON(t *testing.T) {
	largeBody := `{"device_name":"` + strings.Repeat("a", 4060) + `","platform":"macos"}`
	if len(largeBody) != 4097 {
		t.Fatalf("large body length = %d", len(largeBody))
	}
	tests := []struct {
		name        string
		contentType string
		body        io.Reader
	}{
		{name: "unknown field", contentType: "application/json", body: strings.NewReader(`{"device_name":"Mac","platform":"macos","extra":true}`)},
		{name: "trailing value", contentType: "application/json", body: strings.NewReader(`{"device_name":"Mac","platform":"macos"}{}`)},
		{name: "null", contentType: "application/json", body: strings.NewReader(`null`)},
		{name: "array", contentType: "application/json", body: strings.NewReader(`[{"device_name":"Mac","platform":"macos"}]`)},
		{name: "wrong media type", contentType: "text/plain", body: strings.NewReader(`{"device_name":"Mac","platform":"macos"}`)},
		{name: "too large", contentType: "application/json", body: strings.NewReader(largeBody)},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			api := &fakeIdentityAPI{}
			request := httptest.NewRequest(http.MethodPost, "/v1/workspaces", item.body)
			request.Header.Set("Content-Type", item.contentType)
			request.Header.Set("Idempotency-Key", "00000000-0000-4000-8000-000000000901")
			response := httptest.NewRecorder()
			NewApplicationHandler(nil, api, nil).ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":{\"code\":\"invalid_request\"}}\n" {
				t.Fatalf("status/body = %d/%q", response.Code, response.Body.String())
			}
			if api.createCalls != 0 {
				t.Fatalf("createCalls = %d", api.createCalls)
			}
		})
	}
}

func TestWorkspaceCreateRejectsAmbiguousJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "case mismatched fields", body: `{"DEVICE_NAME":"Mac","PLATFORM":"macos"}`},
		{name: "invalid UTF-8", body: `{"device_name":"Mac` + string([]byte{0xff}) + `","platform":"macos"}`},
		{name: "duplicate top-level field", body: `{"device_name":"Mac","device_name":"Other Mac","platform":"macos"}`},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			api := &fakeIdentityAPI{}
			request := httptest.NewRequest(http.MethodPost, "/v1/workspaces", strings.NewReader(item.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "00000000-0000-4000-8000-000000000907")
			response := httptest.NewRecorder()

			NewApplicationHandler(nil, api, nil).ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":{\"code\":\"invalid_request\"}}\n" {
				t.Fatalf("status/body = %d/%q", response.Code, response.Body.String())
			}
			if api.createCalls != 0 {
				t.Fatalf("createCalls = %d", api.createCalls)
			}
		})
	}
}

func TestWorkspaceCreateRejectsDuplicateContentType(t *testing.T) {
	api := &fakeIdentityAPI{}
	request := httptest.NewRequest(http.MethodPost, "/v1/workspaces", strings.NewReader(`{"device_name":"Mac","platform":"macos"}`))
	request.Header.Add("Content-Type", "application/json")
	request.Header.Add("Content-Type", "text/plain")
	request.Header.Set("Idempotency-Key", "00000000-0000-4000-8000-000000000908")
	response := httptest.NewRecorder()

	NewApplicationHandler(nil, api, nil).ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":{\"code\":\"invalid_request\"}}\n" {
		t.Fatalf("status/body = %d/%q", response.Code, response.Body.String())
	}
	if api.createCalls != 0 {
		t.Fatalf("createCalls = %d", api.createCalls)
	}
}

func TestConnectorCannotListDevices(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/devices", nil)
	request.Header.Set("Authorization", "Bearer connector-runtime-marker")
	response := httptest.NewRecorder()
	NewApplicationHandler(nil, &fakeIdentityAPI{}, nil).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || response.Body.String() != "{\"error\":{\"code\":\"forbidden\"}}\n" {
		t.Fatalf("status/body = %d/%q", response.Code, response.Body.String())
	}
}

func TestConnectorCannotRevokeDevice(t *testing.T) {
	request := httptest.NewRequest(http.MethodDelete, "/v1/devices/00000000-0000-4000-8000-000000000003", nil)
	request.Header.Set("Authorization", "Bearer connector-runtime-marker")
	request.Header.Set("Idempotency-Key", "00000000-0000-4000-8000-000000000904")
	response := httptest.NewRecorder()
	NewApplicationHandler(nil, &fakeIdentityAPI{}, nil).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || response.Body.String() != "{\"error\":{\"code\":\"forbidden\"}}\n" {
		t.Fatalf("status/body = %d/%q", response.Code, response.Body.String())
	}
}

func TestDuplicateSecurityHeadersAreRejected(t *testing.T) {
	t.Run("authorization", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/v1/devices", nil)
		request.Header.Add("Authorization", "Bearer connector-runtime-marker")
		request.Header.Add("Authorization", "Bearer second-runtime-marker")
		response := httptest.NewRecorder()
		NewApplicationHandler(nil, &fakeIdentityAPI{}, nil).ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || response.Body.String() != "{\"error\":{\"code\":\"unauthorized\"}}\n" {
			t.Fatalf("status/body = %d/%q", response.Code, response.Body.String())
		}
	})
	t.Run("idempotency", func(t *testing.T) {
		api := &fakeIdentityAPI{}
		request := httptest.NewRequest(http.MethodPost, "/v1/workspaces", strings.NewReader(`{"device_name":"Mac","platform":"macos"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Add("Idempotency-Key", "00000000-0000-4000-8000-000000000905")
		request.Header.Add("Idempotency-Key", "00000000-0000-4000-8000-000000000906")
		response := httptest.NewRecorder()
		NewApplicationHandler(nil, api, nil).ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":{\"code\":\"invalid_request\"}}\n" {
			t.Fatalf("status/body = %d/%q", response.Code, response.Body.String())
		}
		if api.createCalls != 0 {
			t.Fatalf("createCalls = %d", api.createCalls)
		}
	})
}

func TestV1MethodGuardRecognizesEveryRouteShape(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		path          string
		wantStatus    int
		wantAllow     string
		wantDelegated bool
	}{
		{name: "workspace create", method: http.MethodPost, path: "/v1/workspaces", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "pairing create", method: http.MethodPost, path: "/v1/pairing-requests", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "pairing lookup", method: http.MethodPost, path: "/v1/pairing-requests/lookup", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "pairing details", method: http.MethodGet, path: "/v1/pairing-requests/00000000-0000-4000-8000-000000000301", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "pairing approval", method: http.MethodPost, path: "/v1/pairing-requests/00000000-0000-4000-8000-000000000301/approve", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "pairing claim", method: http.MethodPost, path: "/v1/pairing-requests/00000000-0000-4000-8000-000000000301/claim", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "device list", method: http.MethodGet, path: "/v1/devices", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "device rename", method: http.MethodPatch, path: "/v1/devices/00000000-0000-4000-8000-000000000201", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "device revoke", method: http.MethodDelete, path: "/v1/devices/00000000-0000-4000-8000-000000000201", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "recovery", method: http.MethodPost, path: "/v1/recoveries", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "non v1 health", method: http.MethodGet, path: "/readyz", wantStatus: http.StatusNoContent, wantDelegated: true},
		{name: "workspace wrong method", method: http.MethodGet, path: "/v1/workspaces", wantStatus: http.StatusMethodNotAllowed, wantAllow: http.MethodPost},
		{name: "pairing details head", method: http.MethodHead, path: "/v1/pairing-requests/00000000-0000-4000-8000-000000000301", wantStatus: http.StatusMethodNotAllowed, wantAllow: http.MethodGet},
		{name: "device list head", method: http.MethodHead, path: "/v1/devices", wantStatus: http.StatusMethodNotAllowed, wantAllow: http.MethodGet},
		{name: "device dynamic wrong method", method: http.MethodGet, path: "/v1/devices/00000000-0000-4000-8000-000000000201", wantStatus: http.StatusMethodNotAllowed, wantAllow: "PATCH, DELETE"},
		{name: "static lookup is not dynamic details", method: http.MethodGet, path: "/v1/pairing-requests/lookup", wantStatus: http.StatusMethodNotAllowed, wantAllow: http.MethodPost},
		{name: "unknown path", method: http.MethodGet, path: "/v1/not-a-route", wantStatus: http.StatusNotFound},
		{name: "extra dynamic segment", method: http.MethodGet, path: "/v1/devices/id/extra", wantStatus: http.StatusNotFound},
		{name: "v1 root", method: http.MethodGet, path: "/v1/", wantStatus: http.StatusNotFound},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			delegated := 0
			handler := v1MethodGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				delegated++
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(item.method, item.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != item.wantStatus || response.Header().Get("Allow") != item.wantAllow {
				t.Fatalf("status/Allow = %d/%q", response.Code, response.Header().Get("Allow"))
			}
			if item.wantDelegated {
				if delegated != 1 || response.Body.Len() != 0 {
					t.Fatalf("delegated/body bytes = %d/%d", delegated, response.Body.Len())
				}

				return
			}
			wantBody := "{\"error\":{\"code\":\"invalid_request\"}}\n"
			if item.wantStatus == http.StatusNotFound {
				wantBody = "{\"error\":{\"code\":\"not_found\"}}\n"
			}
			if delegated != 0 || response.Body.String() != wantBody {
				t.Fatalf("delegated/body metadata = %d/%d", delegated, response.Body.Len())
			}
		})
	}
}

func TestClientIPTrustsForwardingOnlyFromConfiguredProxy(t *testing.T) {
	_, trusted, err := net.ParseCIDR("127.0.0.1/32")
	if err != nil {
		t.Fatalf("ParseCIDR() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "127.0.0.1:4000"
	request.Header.Set("X-Forwarded-For", "192.0.2.44, 127.0.0.1")
	if got := clientIP(request, []*net.IPNet{trusted}); got != "192.0.2.44" {
		t.Fatalf("trusted clientIP = %q", got)
	}
	request.RemoteAddr = "198.51.100.9:4000"
	if got := clientIP(request, []*net.IPNet{trusted}); got != "198.51.100.9" {
		t.Fatalf("untrusted clientIP = %q", got)
	}
}
