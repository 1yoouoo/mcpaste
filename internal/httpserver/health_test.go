package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLivez(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/livez", nil)
	response := httptest.NewRecorder()

	NewHandler(nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
	if body := response.Body.String(); body != "{\"status\":\"ok\"}\n" {
		t.Fatalf("body = %q, want %q", body, "{\"status\":\"ok\"}\n")
	}
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name       string
		readiness  ReadinessFunc
		wantStatus int
		wantBody   string
	}{
		{
			name: "ready",
			readiness: func(_ context.Context) error {
				return nil
			},
			wantStatus: http.StatusOK,
			wantBody:   "{\"status\":\"ok\"}\n",
		},
		{
			name: "unavailable",
			readiness: func(_ context.Context) error {
				return errors.New("database-password-secret")
			},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "{\"status\":\"unavailable\"}\n",
		},
		{
			name: "database detail redacted",
			readiness: func(_ context.Context) error {
				return errors.New("postgres-password-secret-marker")
			},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "{\"status\":\"unavailable\"}\n",
		},
	}

	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			response := httptest.NewRecorder()

			NewHandler(item.readiness).ServeHTTP(response, request)

			if response.Code != item.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, item.wantStatus)
			}
			if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", contentType)
			}
			if body := response.Body.String(); body != item.wantBody {
				t.Fatalf("response body bytes = %d, want %d", len(body), len(item.wantBody))
			}
			for markerIndex, marker := range []string{"database-password-secret", "postgres-password-secret-marker"} {
				if strings.Contains(response.Body.String(), marker) {
					t.Fatalf("response contains readiness marker index %d", markerIndex)
				}
			}
		})
	}
}

func TestLivezRejectsNonGET(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/livez", nil)
	response := httptest.NewRecorder()

	NewHandler(nil).ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
	if allow := response.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("Allow = %q, want %q", allow, http.MethodGet)
	}
}
