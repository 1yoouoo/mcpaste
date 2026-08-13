package secure

import (
	"encoding/base64"
	"errors"
)

var errInvalidRawURL = errors.New("raw URL-base64 value is invalid")

func decodeCanonicalRawURL(value string, expectedBytes int) ([]byte, error) {
	if expectedBytes < 0 || len(value) != base64.RawURLEncoding.EncodedLen(expectedBytes) {
		return nil, errInvalidRawURL
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != expectedBytes {
		return nil, errInvalidRawURL
	}
	if base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errInvalidRawURL
	}
	return decoded, nil
}
