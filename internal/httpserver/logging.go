package httpserver

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

func NewAccessLogMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			response := &statusWriter{ResponseWriter: w, status: http.StatusOK}

			defer func() {
				panicValue := recover()
				if panicValue != nil {
					response.status = http.StatusInternalServerError
				}
				logger.Info("http request",
					slog.String("method", r.Method),
					slog.String("path", safeRoute(r.Pattern)),
					slog.Int("status", response.status),
					slog.Int64("duration_ms", time.Since(started).Milliseconds()),
				)
				if panicValue != nil {
					panic(panicValue)
				}
			}()

			next.ServeHTTP(response, r)
		})
	}
}

func NewRecoveryMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1" || strings.HasPrefix(r.URL.Path, "/v1/") {
				serveV1WithRecovery(logger, next, w, r)
				return
			}
			defer func() {
				if recover() != nil {
					logRecoveredPanic(logger, r)
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func serveV1WithRecovery(logger *slog.Logger, next http.Handler, w http.ResponseWriter, r *http.Request) {
	buffered := &bufferedResponse{header: make(http.Header), status: http.StatusOK}
	defer func() {
		if recover() != nil {
			logRecoveredPanic(logger, r)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": map[string]string{"code": "internal_error"},
			})
			return
		}
		buffered.flush(w)
	}()
	next.ServeHTTP(buffered, r)
}

func logRecoveredPanic(logger *slog.Logger, r *http.Request) {
	logger.Error("http panic recovered",
		slog.String("method", r.Method),
		slog.String("path", safeRoute(r.Pattern)),
	)
}

type bufferedResponse struct {
	header        http.Header
	body          bytes.Buffer
	informational []int
	status        int
	wroteHeader   bool
}

func (w *bufferedResponse) Header() http.Header {
	return w.header
}

func (w *bufferedResponse) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	if status < 100 || status > 999 {
		panic(fmt.Sprintf("invalid WriteHeader code %v", status))
	}
	if status >= 100 && status <= 199 {
		w.informational = append(w.informational, status)
		return
	}
	w.status = status
	w.wroteHeader = true
}

func (w *bufferedResponse) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(data)
}

func (w *bufferedResponse) flush(target http.ResponseWriter) {
	for name, values := range w.header {
		for _, value := range values {
			target.Header().Add(name, value)
		}
	}
	for _, status := range w.informational {
		target.WriteHeader(status)
	}
	target.WriteHeader(w.status)
	_, _ = target.Write(w.body.Bytes())
}

func safeRoute(pattern string) string {
	if pattern == "" {
		return "<unmatched>"
	}
	return pattern
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	if status >= 100 && status <= 199 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
