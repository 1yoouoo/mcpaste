package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return identityInvalid()
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return identityInvalid()
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return identityInvalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return identityInvalid()
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return identityInvalid()
	}
	return nil
}

func requireEmptyBody(r *http.Request) error {
	if r.Body == nil {
		return nil
	}
	buffer := make([]byte, 1)
	count, err := r.Body.Read(buffer)
	if count != 0 || err != io.EOF {
		return identityInvalid()
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeResult(w http.ResponseWriter, result identity.Result) {
	if result.Status == http.StatusNoContent {
		w.WriteHeader(result.Status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(result.Status)
	_, _ = w.Write(result.Body)
}

func identityInvalid() error { return identity.ErrInvalid }
