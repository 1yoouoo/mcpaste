package images

import (
	"bytes"
	"fmt"
	"os"
	"testing"

	"github.com/1yoouoo/mcpaste/internal/secure"
)

// TestS3StoreAgainstLiveEndpoint exercises real SigV4 signing against an S3-compatible
// service. It is skipped unless the MCPASTE_S3_* environment is configured, so ordinary
// test runs stay offline. It writes only under its own workspace prefix and cleans up.
func TestS3StoreAgainstLiveEndpoint(t *testing.T) {
	cfg := S3Config{
		Endpoint:  os.Getenv("MCPASTE_S3_ENDPOINT"),
		Region:    os.Getenv("MCPASTE_S3_REGION"),
		Bucket:    os.Getenv("MCPASTE_S3_BUCKET"),
		Prefix:    os.Getenv("MCPASTE_S3_PREFIX"),
		AccessKey: os.Getenv("MCPASTE_S3_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("MCPASTE_S3_SECRET_ACCESS_KEY"),
	}
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		t.Skip("live object storage environment is not configured")
	}

	keyring, err := secure.NewKeyring(
		"image-live",
		map[string][]byte{"image-live": bytes.Repeat([]byte{0x24}, 32)},
		&storageBytes{value: 7},
	)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewS3Store(cfg, keyring, nil)
	if err != nil {
		t.Fatal(err)
	}

	workspace := "00000000-0000-4000-8000-0000000009f1"
	paste := "00000000-0000-4000-8000-0000000009f2"
	revision := "00000000-0000-4000-8000-0000000009f3"
	t.Cleanup(func() {
		if err := store.RemovePaste(workspace, paste); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})

	plaintext := []byte("live round trip payload")
	asset, err := store.Put(workspace, paste, revision, 0, plaintext)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	wantKey := fmt.Sprintf("%s/%s/%s/asset-00.bin", workspace, paste, revision)
	if asset.StorageKey != wantKey {
		t.Fatalf("storage key = %q, want %q", asset.StorageKey, wantKey)
	}

	restored, err := store.Open(asset)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(restored, plaintext) {
		t.Fatalf("restored = %q, want %q", restored, plaintext)
	}

	if _, err := store.Put(workspace, paste, revision, 1, []byte("second asset")); err != nil {
		t.Fatalf("put second: %v", err)
	}
	if err := store.RemoveTree(workspace, paste, revision); err != nil {
		t.Fatalf("remove tree: %v", err)
	}
	if _, err := store.Open(asset); err != ErrUnavailable {
		t.Fatalf("open after removal = %v, want ErrUnavailable", err)
	}
}
