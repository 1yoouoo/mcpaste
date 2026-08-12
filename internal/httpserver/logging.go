package httpserver

import (
	"log/slog"
	"net/http"
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

			next.ServeHTTP(response, r)

			logger.Info("http request",
				slog.String("method", r.Method),
				slog.String("path", safeRoute(r.Pattern)),
				slog.Int("status", response.status),
				slog.Int64("duration_ms", time.Since(started).Milliseconds()),
			)
		})
	}
}

func NewRecoveryMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recover() != nil {
					logger.Error("http panic recovered",
						slog.String("method", r.Method),
						slog.String("path", safeRoute(r.Pattern)),
					)
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
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
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
