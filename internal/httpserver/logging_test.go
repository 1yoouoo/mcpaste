package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
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

func TestRecoveryMiddlewareRecoversInvalidBufferedStatus(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	next := http.NewServeMux()
	next.HandleFunc("GET /v1/invalid-status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Unsafe-Marker", "invalid-status-header-secret")
		w.WriteHeader(99)
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/invalid-status", nil)

	escaped := false
	func() {
		defer func() {
			escaped = recover() != nil
		}()
		NewRecoveryMiddleware(logger)(next).ServeHTTP(response, request)
	}()

	if escaped {
		t.Fatal("invalid status panic escaped recovery")
	}
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
		t.Fatal("invalid status response retained buffered handler header")
	}

	entries := decodeLogEntries(t, logs.Bytes())
	if len(entries) != 1 || entries[0]["msg"] != "http panic recovered" {
		t.Fatalf("recovery log entries = %d", len(entries))
	}
	if strings.Contains(logs.String(), "invalid WriteHeader code 99") || strings.Contains(logs.String(), "invalid-status-header-secret") {
		t.Fatal("recovery log contains panic or header value")
	}
}

func TestProductionMiddlewareLogsPanicsAsFinal500(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	next := http.NewServeMux()
	next.HandleFunc("POST /v1/example/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Unsafe-Marker", "panic-response-header-secret")
		_, _ = w.Write([]byte("panic-response-body-secret"))
		panic("panic-value-secret")
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/example/path-secret?token=query-secret", strings.NewReader("request-body-secret"))
	request.Header.Set("Authorization", "Bearer authorization-secret")
	response := httptest.NewRecorder()
	handler := NewRecoveryMiddleware(logger)(NewAccessLogMiddleware(logger)(next))

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError || response.Body.String() != "{\"error\":{\"code\":\"internal_error\"}}\n" {
		t.Fatalf("panic response status/body bytes = %d/%d", response.Code, response.Body.Len())
	}
	if response.Header().Get("X-Unsafe-Marker") != "" {
		t.Fatal("panic response retained buffered handler header")
	}

	entries := decodeLogEntries(t, logs.Bytes())
	if len(entries) != 2 {
		t.Fatalf("log entries = %d, want 2", len(entries))
	}
	accessCount := 0
	recoveryCount := 0
	for _, entry := range entries {
		switch entry["msg"] {
		case "http request":
			accessCount++
			if entry["status"] != float64(http.StatusInternalServerError) {
				t.Fatalf("access status = %v, want %d", entry["status"], http.StatusInternalServerError)
			}
		case "http panic recovered":
			recoveryCount++
		}
	}
	if accessCount != 1 || recoveryCount != 1 {
		t.Fatalf("access/recovery log counts = %d/%d", accessCount, recoveryCount)
	}
	markers := []string{
		"panic-value-secret", "panic-response-body-secret", "panic-response-header-secret",
		"path-secret", "query-secret", "request-body-secret", "authorization-secret", "Authorization",
	}
	for index, marker := range markers {
		if strings.Contains(logs.String(), marker) || strings.Contains(response.Body.String(), marker) {
			t.Fatalf("panic boundary contains secret marker index %d", index)
		}
	}
}

func TestProductionMiddlewarePreservesEarlyHintsBeforeFinalResponse(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	next := http.NewServeMux()
	next.HandleFunc("GET /v1/early-hints", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", "</style.css>; rel=preload; as=style")
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
	})
	handler := NewRecoveryMiddleware(logger)(NewAccessLogMiddleware(logger)(next))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/early-hints", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	var informational []int
	trace := &httptrace.ClientTrace{
		Got1xxResponse: func(code int, _ textproto.MIMEHeader) error {
			informational = append(informational, code)
			return nil
		},
	}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}

	if len(informational) != 1 || informational[0] != http.StatusEarlyHints {
		t.Fatalf("informational statuses = %v, want [%d]", informational, http.StatusEarlyHints)
	}
	if response.StatusCode != http.StatusCreated || string(body) != "created" {
		t.Fatalf("final status/body bytes = %d/%d", response.StatusCode, len(body))
	}
	entries := decodeLogEntries(t, logs.Bytes())
	if len(entries) != 1 || entries[0]["msg"] != "http request" {
		t.Fatalf("access log entries = %d", len(entries))
	}
	if entries[0]["status"] != float64(http.StatusCreated) {
		t.Fatalf("access status = %v, want %d", entries[0]["status"], http.StatusCreated)
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

func decodeLogEntries(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	var entries []map[string]any
	for {
		var entry map[string]any
		err := decoder.Decode(&entry)
		if err == io.EOF {
			return entries
		}
		if err != nil {
			t.Fatalf("decode log entry: %v", err)
		}
		entries = append(entries, entry)
	}
}
