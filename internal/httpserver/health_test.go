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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			response := httptest.NewRecorder()

			NewHandler(tt.readiness).ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantStatus)
			}
			if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", contentType)
			}
			if body := response.Body.String(); body != tt.wantBody {
				t.Fatalf("body = %q, want %q", body, tt.wantBody)
			}
			if strings.Contains(response.Body.String(), "database-password-secret") {
				t.Fatal("response contains readiness error")
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
