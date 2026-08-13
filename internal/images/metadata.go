package images

import (
	"errors"
	"fmt"
)

const (
	MaxSourceBytes = 250 << 20
	MaxBundleItems = 20
)

var ErrInvalidImage = errors.New("invalid image bundle")

type AssetInput struct {
	MIMEType string
	Width    int
	Height   int
	Bytes    []byte
}

func ValidateBundle(items []AssetInput) error {
	if len(items) == 0 || len(items) > MaxBundleItems {
		return ErrInvalidImage
	}
	for _, item := range items {
		if !supportedMIME(item.MIMEType) || item.Width < 1 || item.Height < 1 || len(item.Bytes) == 0 || len(item.Bytes) > MaxSourceBytes {
			return ErrInvalidImage
		}
	}
	return nil
}

func supportedMIME(value string) bool {
	switch value {
	case "image/jpeg", "image/png", "image/heic", "image/heif", "image/webp", "image/tiff", "image/bmp":
		return true
	default:
		return false
	}
}

func validateIdentifiers(workspaceID, pasteID, revisionID string, index int) error {
	if !validUUID(workspaceID) || !validUUID(pasteID) || !validUUID(revisionID) || index < 0 || index >= MaxBundleItems {
		return fmt.Errorf("%w: identifier", ErrInvalidImage)
	}
	return nil
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, r := range value {
		if (index == 8 || index == 13 || index == 18 || index == 23) && r == '-' {
			continue
		}
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}
