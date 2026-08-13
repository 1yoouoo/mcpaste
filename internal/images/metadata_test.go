package images

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestValidateBundleEnforcesStaticImageLimits(t *testing.T) {
	valid := AssetInput{MIMEType: "image/png", Width: 1, Height: 1, Bytes: mustPNG()}
	if err := ValidateBundle([]AssetInput{valid}); err != nil {
		t.Fatalf("ValidateBundle(valid) error = %v", err)
	}
	if err := ValidateBundle(nil); err == nil {
		t.Fatal("empty bundle accepted")
	}
	if err := ValidateBundle([]AssetInput{{MIMEType: "image/gif", Width: 1, Height: 1, Bytes: []byte("animated")}}); err == nil {
		t.Fatal("unsupported image accepted")
	}
	if err := ValidateBundle([]AssetInput{{MIMEType: "image/png", Width: 0, Height: 1, Bytes: mustPNG()}}); err == nil {
		t.Fatal("invalid dimensions accepted")
	}
	if err := ValidateBundle([]AssetInput{{MIMEType: "image/png", Width: 1, Height: 1, Bytes: bytes.Repeat(mustPNG(), MaxBundleBytes/len(mustPNG())+1)}}); err == nil {
		t.Fatal("oversized image bundle accepted")
	}
}

func mustPNG() []byte {
	value, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		panic(err)
	}
	return value
}
