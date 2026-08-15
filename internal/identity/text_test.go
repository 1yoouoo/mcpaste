package identity_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	identitypostgres "github.com/1yoouoo/mcpaste/internal/identity/postgres"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/1yoouoo/mcpaste/internal/testdb"
)

type componentSyncStore struct {
	identity.Store
	tx    *componentSyncTx
	calls int
}

func (store *componentSyncStore) WithinTx(ctx context.Context, run func(identity.TxStore) error) error {
	store.calls++
	return run(store.tx)
}

type componentSyncTx struct {
	identity.TxStore
	result       identity.SyncResult
	assets       map[string][]identity.ImageAsset
	after        int64
	limit        int
	assetLookups []string
}

func (tx *componentSyncTx) Sync(_ context.Context, _ string, after int64, limit int, _ time.Time) (identity.SyncResult, error) {
	tx.after = after
	tx.limit = limit
	return tx.result, nil
}

func (tx *componentSyncTx) ListImageAssets(_ context.Context, _, pasteID, revisionID string) ([]identity.ImageAsset, error) {
	tx.assetLookups = append(tx.assetLookups, pasteID+":"+revisionID)
	assets := tx.assets[revisionID]
	return append([]identity.ImageAsset(nil), assets...), nil
}

type textCounter struct{ next byte }

func (r *textCounter) Read(target []byte) (int, error) {
	for index := range target {
		target[index] = r.next
		r.next++
	}
	return len(target), nil
}

func TestCreatePastePreservesExactText(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := identitypostgres.New(pool)
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000501"
	deviceID := "00000000-0000-4000-8000-000000000502"
	if err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, now); err != nil {
			return err
		}
		_, err := tx.InsertDevice(ctx, workspaceID, identity.Device{
			ID: deviceID, DisplayName: "Text Mac", Platform: "macos", Role: "full", CreatedAt: now,
		})
		return err
	}); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	random := &textCounter{next: 1}
	keyring, err := secure.NewKeyring("text-test", map[string][]byte{"text-test": bytes.Repeat([]byte{0x21}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	service := identity.NewService(store, keyring, random, fixedClock{value: now})
	exact := "  line one\r\nline two\n끝  "
	result, err := service.CreatePaste(ctx, identity.Principal{
		WorkspaceID: workspaceID, DeviceID: deviceID, Scope: "full",
	}, "00000000-0000-4000-8000-000000000503", identity.CreatePasteInput{Text: exact})
	if err != nil {
		t.Fatalf("CreatePaste() error = %v", err)
	}
	var response identity.PasteResponse
	if err := json.Unmarshal(result.Body, &response); err != nil {
		t.Fatalf("decode CreatePaste() response: %v", err)
	}
	if result.Status != 201 || response.Text == nil || *response.Text != exact {
		t.Fatalf("CreatePaste() status/body = %d/%q", result.Status, result.Body)
	}
}

func TestUpdateDeleteAndSequenceOrdering(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := identitypostgres.New(pool)
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000511"
	deviceID := "00000000-0000-4000-8000-000000000512"
	if err := seedTextWorkspace(ctx, store, workspaceID, deviceID, now); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	random := &textCounter{next: 1}
	keyring, err := secure.NewKeyring("text-test", map[string][]byte{"text-test": bytes.Repeat([]byte{0x22}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	service := identity.NewService(store, keyring, random, fixedClock{value: now})
	principal := identity.Principal{WorkspaceID: workspaceID, DeviceID: deviceID, Scope: "full"}
	created, err := service.CreatePaste(ctx, principal, "00000000-0000-4000-8000-000000000513", identity.CreatePasteInput{Text: "first"})
	if err != nil {
		t.Fatalf("CreatePaste() error = %v", err)
	}
	var createdBody identity.PasteResponse
	if err := json.Unmarshal(created.Body, &createdBody); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	updated, err := service.UpdatePaste(ctx, principal, createdBody.PasteID, "00000000-0000-4000-8000-000000000514", identity.UpdatePasteInput{Text: "offline edit"})
	if err != nil {
		t.Fatalf("UpdatePaste() error = %v", err)
	}
	var updatedBody identity.PasteResponse
	if err := json.Unmarshal(updated.Body, &updatedBody); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if updatedBody.ServerSequence <= createdBody.ServerSequence || updatedBody.RevisionID == createdBody.RevisionID || updatedBody.Text == nil || *updatedBody.Text != "offline edit" {
		t.Fatalf("update metadata = %#v", updatedBody)
	}
	deleted, err := service.DeletePaste(ctx, principal, createdBody.PasteID, "00000000-0000-4000-8000-000000000515")
	if err != nil || deleted.Status != 204 || len(deleted.Body) != 0 {
		t.Fatalf("DeletePaste() = %d/%q/%v", deleted.Status, deleted.Body, err)
	}
	if pastes, err := service.ListPastes(ctx, principal); err != nil || len(pastes) != 0 {
		t.Fatalf("ListPastes() after tombstone = %#v/%v", pastes, err)
	}
	latest, err := service.LatestPaste(ctx, identity.Principal{WorkspaceID: workspaceID, DeviceID: "00000000-0000-4000-8000-000000000599", Scope: "connector"})
	if err != nil || latest.Available {
		t.Fatalf("LatestPaste() after tombstone = %#v/%v", latest, err)
	}
	var revisionCount, eventCount int
	if err := pool.QueryRow(ctx, "select count(*) from paste_revisions where workspace_id = $1::uuid", workspaceID).Scan(&revisionCount); err != nil {
		t.Fatalf("count revisions: %v", err)
	}
	if err := pool.QueryRow(ctx, "select count(*) from workspace_events where workspace_id = $1::uuid and event_type like 'paste.%'", workspaceID).Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if revisionCount != 3 || eventCount != 3 {
		t.Fatalf("revision/event counts = %d/%d", revisionCount, eventCount)
	}
	var eventTypes []string
	rows, err := pool.Query(ctx, "select event_type from workspace_events where workspace_id = $1::uuid order by sequence", workspaceID)
	if err != nil {
		t.Fatalf("query event types: %v", err)
	}
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			rows.Close()
			t.Fatalf("scan event type: %v", err)
		}
		eventTypes = append(eventTypes, eventType)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("event type rows: %v", err)
	}
	if want := []string{"paste.created", "paste.revised", "paste.deleted"}; !equalStrings(eventTypes, want) {
		t.Fatalf("event types = %#v, want %#v", eventTypes, want)
	}
}

func TestSyncReturnsOrderedEventsAndTombstones(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := identitypostgres.New(pool)
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000531"
	deviceID := "00000000-0000-4000-8000-000000000532"
	if err := seedTextWorkspace(ctx, store, workspaceID, deviceID, now); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	random := &textCounter{next: 1}
	keyring, err := secure.NewKeyring("text-test", map[string][]byte{"text-test": bytes.Repeat([]byte{0x24}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	service := identity.NewService(store, keyring, random, fixedClock{value: now})
	principal := identity.Principal{WorkspaceID: workspaceID, DeviceID: deviceID, Scope: "full"}
	created, err := service.CreatePaste(ctx, principal, "00000000-0000-4000-8000-000000000533", identity.CreatePasteInput{Text: "sync text"})
	if err != nil {
		t.Fatalf("CreatePaste() error = %v", err)
	}
	var createdBody identity.PasteResponse
	if err := json.Unmarshal(created.Body, &createdBody); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if _, err := service.DeletePaste(ctx, principal, createdBody.PasteID, "00000000-0000-4000-8000-000000000534"); err != nil {
		t.Fatalf("DeletePaste() error = %v", err)
	}
	first, err := service.Sync(ctx, principal, 0, 1)
	if err != nil {
		t.Fatalf("Sync() first page error = %v", err)
	}
	if len(first.Events) != 1 || first.Cursor != first.Events[0].Sequence || !first.HasMore || first.Events[0].EventType != "paste.created" || first.Events[0].Text == nil || *first.Events[0].Text != "sync text" {
		t.Fatalf("first sync page = %#v", first)
	}
	second, err := service.Sync(ctx, principal, first.Events[0].Sequence, 1)
	if err != nil {
		t.Fatalf("Sync() second page error = %v", err)
	}
	if second.Cursor != 2 || second.HasMore || len(second.Events) != 1 || second.Events[0].EventType != "paste.deleted" || !second.Events[0].Deleted || second.Events[0].Text != nil {
		t.Fatalf("second sync page = %#v", second)
	}
}

func TestSyncReturnsAttachmentComponentEventsWithoutCollapsingCursor(t *testing.T) {
	now := time.Date(2026, 8, 14, 7, 0, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000551"
	pasteID := "00000000-0000-4000-8000-000000000552"
	textRevisionID := "00000000-0000-4000-8000-000000000553"
	attachmentRevisionID := "00000000-0000-4000-8000-000000000554"
	clearRevisionID := "00000000-0000-4000-8000-000000000555"
	legacyRevisionID := "00000000-0000-4000-8000-000000000556"
	tombstoneRevisionID := "00000000-0000-4000-8000-000000000557"
	exact := "  sync line one\r\nline two\ntrailing  "
	random := &textCounter{next: 1}
	keyring, err := secure.NewKeyring("sync-components", map[string][]byte{
		"sync-components": bytes.Repeat([]byte{0x31}, 32),
	}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	envelope, err := keyring.Encrypt(
		"paste-text", workspaceID+":"+pasteID+":"+textRevisionID, []byte(exact),
	)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	assetExpiry := now.Add(24 * time.Hour)
	tx := &componentSyncTx{
		result: identity.SyncResult{
			Cursor: 20, HasMore: false,
			Events: []identity.SyncEvent{
				{WorkspaceID: workspaceID, Sequence: 11, EventType: "paste.revised", PasteID: pasteID, RevisionID: textRevisionID, RevisionKind: identity.RevisionContent, ServerSequence: 11, CreatedAt: now, ExpiresAt: now.AddDate(1, 0, 0), Envelope: envelope},
				{WorkspaceID: workspaceID, Sequence: 12, EventType: "paste.revised", PasteID: pasteID, RevisionID: attachmentRevisionID, RevisionKind: identity.RevisionAttachmentBundle, ServerSequence: 12, CreatedAt: now.Add(time.Second), ExpiresAt: assetExpiry, Envelope: secure.Envelope{KeyID: "must-not-decrypt"}},
				{WorkspaceID: workspaceID, Sequence: 13, EventType: "paste.revised", PasteID: pasteID, RevisionID: clearRevisionID, RevisionKind: identity.RevisionAttachmentBundle, ServerSequence: 13, CreatedAt: now.Add(2 * time.Second), ExpiresAt: assetExpiry, Envelope: secure.Envelope{KeyID: "must-not-decrypt"}},
				{WorkspaceID: workspaceID, Sequence: 14, EventType: "paste.deleted", PasteID: pasteID, RevisionID: tombstoneRevisionID, RevisionKind: identity.RevisionTombstone, ServerSequence: 14, CreatedAt: now.Add(3 * time.Second), ExpiresAt: now.AddDate(1, 0, 0), Envelope: secure.Envelope{KeyID: "must-not-decrypt"}},
				{WorkspaceID: workspaceID, Sequence: 15, EventType: "paste.created", PasteID: pasteID, RevisionID: legacyRevisionID, RevisionKind: identity.RevisionImageBundle, ServerSequence: 15, CreatedAt: now.Add(4 * time.Second), ExpiresAt: assetExpiry, Envelope: secure.Envelope{KeyID: "must-not-decrypt"}},
			},
		},
		assets: map[string][]identity.ImageAsset{
			attachmentRevisionID: {
				{AssetIndex: 0, MIMEType: "image/png", Width: 1, Height: 2, ByteSize: 30, ExpiresAt: assetExpiry, StorageKey: "secret-storage-key", Bytes: []byte("secret-image-bytes")},
				{AssetIndex: 1, MIMEType: "image/bmp", Width: 3, Height: 4, ByteSize: 31, ExpiresAt: assetExpiry},
			},
			clearRevisionID:  {},
			legacyRevisionID: {{AssetIndex: 0, MIMEType: "image/tiff", Width: 5, Height: 6, ByteSize: 32, ExpiresAt: assetExpiry}},
		},
	}
	store := &componentSyncStore{tx: tx}
	service := identity.NewService(store, keyring, random, fixedClock{value: now})
	response, err := service.Sync(context.Background(), identity.Principal{WorkspaceID: workspaceID, Scope: "full"}, 10, 5)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if response.Cursor != 20 || response.HasMore || len(response.Events) != 5 || tx.after != 10 || tx.limit != 5 {
		t.Fatalf("Sync() cursor/events/input = %#v after:%d limit:%d", response, tx.after, tx.limit)
	}
	if response.Events[0].Kind != identity.RevisionContent || response.Events[0].Text == nil || *response.Events[0].Text != exact || response.Events[0].Assets != nil {
		t.Fatalf("text event = %#v", response.Events[0])
	}
	if response.Events[1].Kind != identity.RevisionAttachmentBundle || response.Events[1].Text != nil || response.Events[1].Assets == nil || len(*response.Events[1].Assets) != 2 || (*response.Events[1].Assets)[1].AssetIndex != 1 {
		t.Fatalf("attachment event = %#v", response.Events[1])
	}
	if response.Events[2].Assets == nil || len(*response.Events[2].Assets) != 0 {
		t.Fatalf("clear event = %#v", response.Events[2])
	}
	if !response.Events[3].Deleted || response.Events[3].Text != nil || response.Events[3].Assets != nil {
		t.Fatalf("tombstone event = %#v", response.Events[3])
	}
	if response.Events[4].Kind != identity.RevisionImageBundle || response.Events[4].Assets == nil || len(*response.Events[4].Assets) != 1 {
		t.Fatalf("legacy event = %#v", response.Events[4])
	}
	if len(tx.assetLookups) != 3 || store.calls != 1 {
		t.Fatalf("asset lookups/transactions = %#v/%d", tx.assetLookups, store.calls)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"secret-storage-key",
		"secret-image-bytes",
		base64.StdEncoding.EncodeToString([]byte("secret-image-bytes")),
		"must-not-decrypt", "ciphertext", "nonce",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("sync JSON exposed %q: %s", forbidden, encoded)
		}
	}
}

func TestPurgeTextDeletesExpiredRevisionsAndOrphanPastes(t *testing.T) {
	ctx := context.Background()
	p := testdb.New(t)
	store := identitypostgres.New(p)
	createdAt := time.Date(2025, 8, 13, 12, 0, 0, 0, time.UTC)
	purgeAt := createdAt.AddDate(1, 0, 0)
	workspaceID := "00000000-0000-4000-8000-000000000541"
	deviceID := "00000000-0000-4000-8000-000000000542"
	if err := seedTextWorkspace(ctx, store, workspaceID, deviceID, createdAt); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	random := &textCounter{next: 1}
	keyring, err := secure.NewKeyring("text-test", map[string][]byte{"text-test": bytes.Repeat([]byte{0x25}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	service := identity.NewService(store, keyring, random, fixedClock{value: createdAt})
	if _, err := service.CreatePaste(ctx, identity.Principal{WorkspaceID: workspaceID, DeviceID: deviceID, Scope: "full"}, "00000000-0000-4000-8000-000000000543", identity.CreatePasteInput{Text: "expire me"}); err != nil {
		t.Fatalf("CreatePaste() error = %v", err)
	}
	revisions, pastes, err := store.PurgeText(ctx, purgeAt)
	if err != nil {
		t.Fatalf("PurgeText() error = %v", err)
	}
	if revisions != 1 || pastes != 1 {
		t.Fatalf("PurgeText() counts = %d/%d, want 1/1", revisions, pastes)
	}
}

func TestPasteMutationReplayIsByteIdenticalAndDoesNotDuplicateRevision(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := identitypostgres.New(pool)
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000521"
	deviceID := "00000000-0000-4000-8000-000000000522"
	if err := seedTextWorkspace(ctx, store, workspaceID, deviceID, now); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	random := &textCounter{next: 1}
	keyring, err := secure.NewKeyring("text-test", map[string][]byte{"text-test": bytes.Repeat([]byte{0x23}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	service := identity.NewService(store, keyring, random, fixedClock{value: now})
	principal := identity.Principal{WorkspaceID: workspaceID, DeviceID: deviceID, Scope: "full"}
	key := "00000000-0000-4000-8000-000000000523"
	first, err := service.CreatePaste(ctx, principal, key, identity.CreatePasteInput{Text: "replay"})
	if err != nil {
		t.Fatalf("first CreatePaste() error = %v", err)
	}
	second, err := service.CreatePaste(ctx, principal, key, identity.CreatePasteInput{Text: "replay"})
	if err != nil || first.Status != second.Status || !bytes.Equal(first.Body, second.Body) {
		t.Fatalf("replay = %d/%d %q/%q %v", first.Status, second.Status, first.Body, second.Body, err)
	}
	var count int
	if err := pool.QueryRow(ctx, "select count(*) from paste_revisions where workspace_id = $1::uuid", workspaceID).Scan(&count); err != nil {
		t.Fatalf("count revisions: %v", err)
	}
	if count != 1 {
		t.Fatalf("revision count = %d, want 1", count)
	}
}

func seedTextWorkspace(ctx context.Context, store identity.Store, workspaceID, deviceID string, now time.Time) error {
	return store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, now); err != nil {
			return err
		}
		_, err := tx.InsertDevice(ctx, workspaceID, identity.Device{
			ID: deviceID, DisplayName: "Text Mac", Platform: "macos", Role: "full", CreatedAt: now,
		})
		return err
	})
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
