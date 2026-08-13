package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	identitypostgres "github.com/1yoouoo/mcpaste/internal/identity/postgres"
	"github.com/1yoouoo/mcpaste/internal/images"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/1yoouoo/mcpaste/internal/testdb"
)

type imageCounter struct{ next byte }

func (r *imageCounter) Read(target []byte) (int, error) {
	for index := range target {
		target[index] = r.next
		r.next++
	}
	return len(target), nil
}

func TestImageBundlePersistsEncryptedAssetsAndMCPReadsLatest(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := identitypostgres.New(pool)
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000701"
	deviceID := "00000000-0000-4000-8000-000000000702"
	if err := seedTextWorkspace(ctx, store, workspaceID, deviceID, now); err != nil {
		t.Fatal(err)
	}
	random := &imageCounter{next: 1}
	keyring, err := secure.NewKeyring("image-test", map[string][]byte{"image-test": bytes.Repeat([]byte{0x33}, 32)}, random)
	if err != nil {
		t.Fatal(err)
	}
	service := identity.NewService(store, keyring, random, fixedClock{value: now})
	fileStore, err := images.NewFileStore(t.TempDir(), keyring)
	if err != nil {
		t.Fatal(err)
	}
	service.SetImageStore(fileStore)
	principal := identity.Principal{WorkspaceID: workspaceID, DeviceID: deviceID, Scope: "full"}
	created, err := service.CreateImagePaste(ctx, principal, "00000000-0000-4000-8000-000000000703", identity.CreateImagePasteInput{Assets: []images.AssetInput{{MIMEType: "image/png", Width: 2, Height: 2, Bytes: []byte("normalized png")}}})
	if err != nil {
		t.Fatal(err)
	}
	var response identity.PasteResponse
	if err := json.Unmarshal(created.Body, &response); err != nil {
		t.Fatal(err)
	}
	if response.Kind != "image_bundle" || len(response.Assets) != 1 || response.ExpiresAt.Sub(response.CreatedAt) != 24*time.Hour {
		t.Fatalf("image response = %#v", response)
	}
	latest, err := service.LatestPaste(ctx, identity.Principal{WorkspaceID: workspaceID, DeviceID: "connector", Scope: "connector"})
	if err != nil {
		t.Fatal(err)
	}
	if !latest.Available || len(latest.Images) != 1 || string(latest.Images[0].Bytes) != "normalized png" {
		t.Fatalf("latest image = %#v", latest)
	}
	if _, err := service.CreateImagePaste(ctx, identity.Principal{WorkspaceID: workspaceID, DeviceID: "connector", Scope: "connector"}, "idempotency", identity.CreateImagePasteInput{Assets: []images.AssetInput{{MIMEType: "image/png", Width: 1, Height: 1, Bytes: []byte("x")}}}); err != identity.ErrForbidden {
		t.Fatalf("connector write error = %v", err)
	}
}
