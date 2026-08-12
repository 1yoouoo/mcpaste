package httpserver

import (
	"context"
	"io"
	"net/http"
)

type ReadinessFunc func(context.Context) error

func NewHandler(readiness ReadinessFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", healthHandler(nil))
	mux.HandleFunc("/readyz", healthHandler(readiness))
	return mux
}

func healthHandler(readiness ReadinessFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		if readiness != nil && readiness(r.Context()) != nil {
			writeHealth(w, http.StatusServiceUnavailable, "unavailable")
			return
		}

		writeHealth(w, http.StatusOK, "ok")
	}
}

func writeHealth(w http.ResponseWriter, status int, value string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "{\"status\":\""+value+"\"}\n")
}
