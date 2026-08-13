package identity

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/1yoouoo/mcpaste/internal/images"
)

func TestImageIdempotencyCanonicalInputIncludesAssetBytes(t *testing.T) {
	first, err := json.Marshal(imageCanonicalInput(CreateImagePasteInput{Assets: []images.AssetInput{{MIMEType: "image/png", Width: 1, Height: 1, Bytes: []byte("a")}}}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(imageCanonicalInput(CreateImagePasteInput{Assets: []images.AssetInput{{MIMEType: "image/png", Width: 1, Height: 1, Bytes: []byte("b")}}}))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("image canonical inputs ignore asset bytes")
	}
}
