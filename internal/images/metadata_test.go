package images

import (
	"bytes"
	"testing"
)

func TestValidateBundleEnforcesStaticImageLimits(t *testing.T) {
	valid := AssetInput{MIMEType: "image/png", Width: 2, Height: 3, Bytes: []byte("normalized")}
	if err := ValidateBundle([]AssetInput{valid}); err != nil {
		t.Fatalf("ValidateBundle(valid) error = %v", err)
	}
	if err := ValidateBundle(nil); err == nil {
		t.Fatal("empty bundle accepted")
	}
	if err := ValidateBundle([]AssetInput{{MIMEType: "image/gif", Width: 1, Height: 1, Bytes: []byte("animated")}}); err == nil {
		t.Fatal("unsupported image accepted")
	}
	if err := ValidateBundle([]AssetInput{{MIMEType: "image/png", Width: 0, Height: 1, Bytes: bytes.Repeat([]byte{'x'}, 10)}}); err == nil {
		t.Fatal("invalid dimensions accepted")
	}
}
