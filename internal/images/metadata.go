package images

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
)

const (
	MaxSourceBytes = 250 << 20
	MaxBundleItems = 20
	MaxBundleBytes = 32 << 20
	MaxDimension   = 100_000
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
	var totalBytes int
	for _, item := range items {
		if !supportedMIME(item.MIMEType) || item.Width < 1 || item.Height < 1 || item.Width > MaxDimension || item.Height > MaxDimension || len(item.Bytes) == 0 || len(item.Bytes) > MaxSourceBytes || !validImageBytes(item) {
			return ErrInvalidImage
		}
		totalBytes += len(item.Bytes)
		if totalBytes > MaxBundleBytes {
			return ErrInvalidImage
		}
	}
	return nil
}

func validImageBytes(item AssetInput) bool {
	config, format, err := image.DecodeConfig(bytes.NewReader(item.Bytes))
	switch item.MIMEType {
	case "image/png", "image/jpeg":
		return err == nil && ((item.MIMEType == "image/png" && format == "png") || (item.MIMEType == "image/jpeg" && format == "jpeg")) && config.Width == item.Width && config.Height == item.Height
	case "image/heic", "image/heif":
		return validHEIF(item.Bytes)
	case "image/webp":
		return validWebP(item.Bytes)
	case "image/tiff":
		return len(item.Bytes) >= 4 && (bytes.Equal(item.Bytes[:4], []byte{'I', 'I', 42, 0}) || bytes.Equal(item.Bytes[:4], []byte{'M', 'M', 0, 42}))
	case "image/bmp":
		if len(item.Bytes) < 26 || !bytes.Equal(item.Bytes[:2], []byte{'B', 'M'}) {
			return false
		}
		return int(binary.LittleEndian.Uint32(item.Bytes[18:22])) == item.Width && int(binary.LittleEndian.Uint32(item.Bytes[22:26])) == item.Height
	default:
		return false
	}
}

func validWebP(value []byte) bool {
	if len(value) < 16 || !bytes.Equal(value[:4], []byte("RIFF")) || !bytes.Equal(value[8:12], []byte("WEBP")) {
		return false
	}
	if bytes.Equal(value[12:16], []byte("VP8X")) {
		return len(value) > 20 && value[20]&0x02 == 0
	}
	return bytes.Equal(value[12:16], []byte("VP8 ")) || bytes.Equal(value[12:16], []byte("VP8L"))
}

func validHEIF(value []byte) bool {
	if len(value) < 12 || !bytes.Equal(value[4:8], []byte("ftyp")) {
		return false
	}
	brand := string(value[8:12])
	for _, accepted := range []string{"heic", "heix", "hevc", "hevx", "mif1", "msf1"} {
		if brand == accepted {
			return true
		}
	}
	return false
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
