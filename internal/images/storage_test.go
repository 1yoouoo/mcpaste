package images

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/1yoouoo/mcpaste/internal/secure"
)

type storageBytes struct{ value byte }

func (r *storageBytes) Read(target []byte) (int, error) {
	for i := range target {
		target[i] = r.value
		r.value++
	}
	return len(target), nil
}

func TestFileStoreEncryptsAndRoundTripsImage(t *testing.T) {
	random := &storageBytes{value: 1}
	keyring, err := secure.NewKeyring("image-test", map[string][]byte{"image-test": bytes.Repeat([]byte{0x42}, 32)}, random)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store, err := NewFileStore(root, keyring)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := store.Put("00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000003", 0, []byte("image bytes"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, asset.StorageKey)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("image bytes")) {
		t.Fatal("plaintext image persisted")
	}
	got, err := store.Open(asset)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "image bytes" {
		t.Fatalf("round trip = %q", got)
	}
}

func TestFileStoreRejectsTraversalIdentifiers(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("../workspace", "paste", "revision", 0, []byte("x")); err == nil {
		t.Fatal("traversal accepted")
	}
}
