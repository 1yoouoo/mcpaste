package identity_test

import (
	"bytes"
	"context"
	"encoding/base64"
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

type imageCreateStore struct {
	identity.Store
	tx *imageCreateTx
}

func (s *imageCreateStore) WithinTx(ctx context.Context, fn func(identity.TxStore) error) error {
	return fn(s.tx)
}

type imageCreateTx struct{ identity.TxStore }

func (*imageCreateTx) LockIdempotency(context.Context, string, string, []byte) error {
	return nil
}

func (*imageCreateTx) GetIdempotency(context.Context, string, string, []byte) (identity.IdempotencyRecord, error) {
	return identity.IdempotencyRecord{}, identity.ErrNotFound
}

func (*imageCreateTx) PutIdempotency(context.Context, identity.IdempotencyRecord) error {
	return nil
}

func (*imageCreateTx) InsertPaste(context.Context, string, string, time.Time) error {
	return nil
}

func (*imageCreateTx) SetPasteKind(context.Context, string, string, string) error {
	return nil
}

func (*imageCreateTx) AppendImageRevision(_ context.Context, workspaceID, pasteID, revisionID, _ string, assets []identity.ImageAsset, createdAt, expiresAt time.Time) (identity.TextRevision, error) {
	return identity.TextRevision{
		WorkspaceID: workspaceID, PasteID: pasteID, RevisionID: revisionID,
		RevisionKind: identity.RevisionImageBundle, ServerSequence: 1,
		CreatedAt: createdAt, ExpiresAt: expiresAt, Assets: assets,
	}, nil
}

func TestCreateImagePasteResponseAssetsUseRevisionExpiry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 13, 21, 0, 0, 987654321, time.FixedZone("KST", 9*60*60))
	random := &imageCounter{next: 1}
	keyring, err := secure.NewKeyring("image-test", map[string][]byte{"image-test": bytes.Repeat([]byte{0x33}, 32)}, random)
	if err != nil {
		t.Fatal(err)
	}
	service := identity.NewService(&imageCreateStore{tx: &imageCreateTx{}}, keyring, random, fixedClock{value: now})
	fileStore, err := images.NewFileStore(t.TempDir(), keyring)
	if err != nil {
		t.Fatal(err)
	}
	service.SetImageStore(fileStore)
	fixture, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	created, err := service.CreateImagePaste(ctx, identity.Principal{
		WorkspaceID: "00000000-0000-4000-8000-000000000701", DeviceID: "00000000-0000-4000-8000-000000000702", Scope: "full",
	}, "00000000-0000-4000-8000-000000000703", identity.CreateImagePasteInput{Assets: []images.AssetInput{{MIMEType: "image/png", Width: 1, Height: 1, Bytes: fixture}}})
	if err != nil {
		t.Fatal(err)
	}
	var response identity.PasteResponse
	if err := json.Unmarshal(created.Body, &response); err != nil {
		t.Fatal(err)
	}
	want := now.Add(identity.ImageLifetime).UTC().Truncate(time.Second)
	if !response.ExpiresAt.Equal(want) || len(response.Assets) != 1 {
		t.Fatalf("created image response = %#v, want one asset expiring at %s", response, want.Format(time.RFC3339))
	}
	for index, asset := range response.Assets {
		if asset.ExpiresAt.IsZero() || !asset.ExpiresAt.Equal(response.ExpiresAt) || asset.ExpiresAt.Location() != time.UTC || asset.ExpiresAt.Nanosecond() != 0 {
			t.Fatalf("created asset %d expires_at = %s, want %s in UTC at second precision", index, asset.ExpiresAt.Format(time.RFC3339Nano), response.ExpiresAt.Format(time.RFC3339))
		}
	}
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
	fixture, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	created, err := service.CreateImagePaste(ctx, principal, "00000000-0000-4000-8000-000000000703", identity.CreateImagePasteInput{Assets: []images.AssetInput{{MIMEType: "image/png", Width: 1, Height: 1, Bytes: fixture}}})
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
	for index, asset := range response.Assets {
		if asset.ExpiresAt.IsZero() || !asset.ExpiresAt.Equal(response.ExpiresAt) || asset.ExpiresAt.Location() != time.UTC || asset.ExpiresAt.Nanosecond() != 0 {
			t.Fatalf("created asset %d expires_at = %s, want %s in UTC at second precision", index, asset.ExpiresAt.Format(time.RFC3339Nano), response.ExpiresAt.Format(time.RFC3339))
		}
	}
	if _, err := service.UpdatePaste(ctx, principal, response.PasteID, "00000000-0000-4000-8000-000000000705", identity.UpdatePasteInput{Text: "must stay an image paste"}); err != identity.ErrInvalid {
		t.Fatalf("text update of image paste error = %v", err)
	}
	latest, err := service.LatestPaste(ctx, identity.Principal{WorkspaceID: workspaceID, DeviceID: "connector", Scope: "connector"})
	if err != nil {
		t.Fatal(err)
	}
	if !latest.Available || len(latest.Images) != 1 || !bytes.Equal(latest.Images[0].Bytes, fixture) {
		t.Fatalf("latest image = %#v", latest)
	}
	if _, err := service.CreateImagePaste(ctx, identity.Principal{WorkspaceID: workspaceID, DeviceID: "connector", Scope: "connector"}, "00000000-0000-4000-8000-000000000704", identity.CreateImagePasteInput{Assets: []images.AssetInput{{MIMEType: "image/png", Width: 1, Height: 1, Bytes: fixture}}}); err != identity.ErrForbidden {
		t.Fatalf("connector write error = %v", err)
	}
}
