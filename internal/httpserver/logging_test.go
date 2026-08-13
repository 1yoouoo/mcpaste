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
	next := http.NewServeMux()
	next.HandleFunc("POST /v1/example/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	body := `{"short_code":"pairing-short-code-marker","recovery_code":"recovery-code-secret-marker","qr_payload":"qr-payload-secret-marker","body":"body-secret"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/example/path-secret?token=query-secret", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer header-secret")
	request.Header.Set("Idempotency-Key", "idempotency-secret-marker")
	request.Header.Set("X-Forwarded-For", "forwarded-for-secret-marker")
	request.Header.Set("Cookie", "pairing-claim-secret-marker")
	response := httptest.NewRecorder()

	NewAccessLogMiddleware(logger)(next).ServeHTTP(response, request)

	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal access log: %v", err)
	}
	if got := entry["method"]; got != http.MethodPost {
		t.Fatalf("method = %v, want %q", got, http.MethodPost)
	}
	if got := entry["path"]; got != "POST /v1/example/{id}" {
		t.Fatalf("path = %v, want %q", got, "POST /v1/example/{id}")
	}
	if got := entry["status"]; got != float64(http.StatusNoContent) {
		t.Fatalf("status = %v, want %d", got, http.StatusNoContent)
	}

	output := logs.String()
	markers := []string{
		"path-secret", "query-secret", "body-secret", "header-secret", "Authorization",
		"idempotency-secret-marker", "pairing-claim-secret-marker", "pairing-short-code-marker",
		"recovery-code-secret-marker", "qr-payload-secret-marker", "forwarded-for-secret-marker",
	}
	for index, marker := range markers {
		if strings.Contains(output, marker) {
			t.Fatalf("access log contains secret marker index %d", index)
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

func TestNewAccessLogMiddlewareLogsUnmatchedRoute(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	request := httptest.NewRequest(http.MethodGet, "/v1/path-secret?token=query-secret", nil)
	response := httptest.NewRecorder()

	NewAccessLogMiddleware(logger)(next).ServeHTTP(response, request)

	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal access log: %v", err)
	}
	if got := entry["status"]; got != float64(http.StatusOK) {
		t.Fatalf("status = %v, want %d", got, http.StatusOK)
	}
	if got := entry["path"]; got != "<unmatched>" {
		t.Fatalf("path = %v, want %q", got, "<unmatched>")
	}
	if strings.Contains(logs.String(), "path-secret") {
		t.Fatalf("access log contains raw path: %s", logs.String())
	}
}

func TestNewRecoveryMiddlewareRecoversWithoutLoggingSecrets(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	next := http.NewServeMux()
	next.HandleFunc("POST /v1/example/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Unsafe-Marker", "response-header-secret")
		_, _ = w.Write([]byte("partial-response-secret"))
		panic("panic-secret")
	})

	body := `{"short_code":"pairing-short-code-marker","recovery_code":"recovery-code-secret-marker","qr_payload":"qr-payload-secret-marker","body":"body-secret"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/example/path-secret?token=query-secret", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer header-secret")
	request.Header.Set("Idempotency-Key", "idempotency-secret-marker")
	request.Header.Set("X-Forwarded-For", "forwarded-for-secret-marker")
	request.Header.Set("Cookie", "pairing-claim-secret-marker")
	response := httptest.NewRecorder()

	NewRecoveryMiddleware(logger)(next).ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if body := response.Body.String(); body != "{\"error\":{\"code\":\"internal_error\"}}\n" {
		t.Fatalf("response body bytes = %d", len(body))
	}
	if response.Header().Get("X-Unsafe-Marker") != "" {
		t.Fatal("panic response retained buffered handler header")
	}

	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal recovery log: %v", err)
	}
	if got := entry["msg"]; got != "http panic recovered" {
		t.Fatalf("message = %v, want %q", got, "http panic recovered")
	}
	if got := entry["method"]; got != http.MethodPost {
		t.Fatalf("method = %v, want %q", got, http.MethodPost)
	}
	if got := entry["path"]; got != "POST /v1/example/{id}" {
		t.Fatalf("path = %v, want %q", got, "POST /v1/example/{id}")
	}

	markers := []string{
		"panic-secret", "path-secret", "query-secret", "body-secret", "header-secret", "Authorization",
		"idempotency-secret-marker", "pairing-claim-secret-marker", "pairing-short-code-marker",
		"recovery-code-secret-marker", "qr-payload-secret-marker", "forwarded-for-secret-marker",
		"partial-response-secret", "response-header-secret",
	}
	for index, marker := range markers {
		if strings.Contains(response.Body.String(), marker) || strings.Contains(response.Header().Get("X-Unsafe-Marker"), marker) || strings.Contains(logs.String(), marker) {
			t.Fatalf("recovery boundary contains secret marker index %d", index)
		}
	}
}

func TestNewRecoveryMiddlewarePreservesFoundationNonV1Response(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("foundation-panic-secret")
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/foundation-panic", nil)

	NewRecoveryMiddleware(logger)(next).ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError || response.Body.String() != "internal server error\n" {
		t.Fatalf("non-v1 status/body bytes = %d/%d", response.Code, response.Body.Len())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/plain; charset=utf-8" {
		t.Fatalf("non-v1 Content-Type = %q", contentType)
	}
	if strings.Contains(logs.String(), "foundation-panic-secret") {
		t.Fatal("non-v1 recovery log contains panic value")
	}
}
