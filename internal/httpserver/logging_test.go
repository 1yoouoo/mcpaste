package httpserver

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewAccessLogMiddlewareLogsMetadataOnly(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/example?token=query-secret", strings.NewReader("body-secret"))
	request.Header.Set("Authorization", "Bearer header-secret")
	response := httptest.NewRecorder()

	NewAccessLogMiddleware(logger)(next).ServeHTTP(response, request)

	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal access log: %v", err)
	}
	if got := entry["method"]; got != http.MethodPost {
		t.Fatalf("method = %v, want %q", got, http.MethodPost)
	}
	if got := entry["path"]; got != "/v1/example" {
		t.Fatalf("path = %v, want %q", got, "/v1/example")
	}
	if got := entry["status"]; got != float64(http.StatusNoContent) {
		t.Fatalf("status = %v, want %d", got, http.StatusNoContent)
	}

	output := logs.String()
	for _, secret := range []string{"query-secret", "body-secret", "header-secret", "Authorization"} {
		if strings.Contains(output, secret) {
			t.Fatalf("access log contains %q: %s", secret, output)
		}
	}
}

func TestNewAccessLogMiddlewareDefaultsStatusToOKOnWrite(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("response"))
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/example", nil)
	response := httptest.NewRecorder()

	NewAccessLogMiddleware(logger)(next).ServeHTTP(response, request)

	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal access log: %v", err)
	}
	if got := entry["status"]; got != float64(http.StatusOK) {
		t.Fatalf("status = %v, want %d", got, http.StatusOK)
	}
}

func TestNewAccessLogMiddlewareDefaultsStatusToOKWithoutResponse(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	request := httptest.NewRequest(http.MethodGet, "/v1/example", nil)
	response := httptest.NewRecorder()

	NewAccessLogMiddleware(logger)(next).ServeHTTP(response, request)

	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal access log: %v", err)
	}
	if got := entry["status"]; got != float64(http.StatusOK) {
		t.Fatalf("status = %v, want %d", got, http.StatusOK)
	}
}
