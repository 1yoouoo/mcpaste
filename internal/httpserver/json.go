package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return identityInvalid()
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" {
		return identityInvalid()
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return identityInvalid()
	}
	if !utf8.Valid(body) || validateObjectKeys(body, target) != nil {
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

func validateObjectKeys(body []byte, target any) error {
	targetType := reflect.TypeOf(target)
	for targetType != nil && targetType.Kind() == reflect.Pointer {
		targetType = targetType.Elem()
	}
	if targetType == nil || targetType.Kind() != reflect.Struct {
		return identityInvalid()
	}

	accepted := make(map[string]struct{}, targetType.NumField())
	for index := 0; index < targetType.NumField(); index++ {
		field := targetType.Field(index)
		if !field.IsExported() {
			continue
		}
		tag, ok := field.Tag.Lookup("json")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		accepted[name] = struct{}{}
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	delimiter, ok := token.(json.Delim)
	if err != nil || !ok || delimiter != '{' {
		return identityInvalid()
	}
	seen := make(map[string]struct{}, len(accepted))
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return identityInvalid()
		}
		if _, ok := accepted[name]; !ok {
			return identityInvalid()
		}
		if _, duplicate := seen[name]; duplicate {
			return identityInvalid()
		}
		seen[name] = struct{}{}

		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return identityInvalid()
		}
	}
	token, err = decoder.Token()
	delimiter, ok = token.(json.Delim)
	if err != nil || !ok || delimiter != '}' {
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
