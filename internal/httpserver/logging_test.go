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
	next.HandleFunc("POST /v1/example/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/example/path-secret?token=query-secret", strings.NewReader("body-secret"))
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
	if got := entry["path"]; got != "POST /v1/example/{id}" {
		t.Fatalf("path = %v, want %q", got, "POST /v1/example/{id}")
	}
	if got := entry["status"]; got != float64(http.StatusNoContent) {
		t.Fatalf("status = %v, want %d", got, http.StatusNoContent)
	}

	output := logs.String()
	for _, secret := range []string{"path-secret", "query-secret", "body-secret", "header-secret", "Authorization"} {
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
	next.HandleFunc("POST /v1/example/{id}", func(w http.ResponseWriter, r *http.Request) {
		panic("panic-secret")
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/example/path-secret?token=query-secret", strings.NewReader("body-secret"))
	request.Header.Set("Authorization", "Bearer header-secret")
	response := httptest.NewRecorder()

	NewRecoveryMiddleware(logger)(next).ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if body := response.Body.String(); body != "internal server error\n" {
		t.Fatalf("body = %q, want %q", body, "internal server error\n")
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

	for _, secret := range []string{"panic-secret", "path-secret", "query-secret", "body-secret", "header-secret", "Authorization"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("response contains %q: %s", secret, response.Body.String())
		}
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("recovery log contains %q: %s", secret, logs.String())
		}
	}
}
