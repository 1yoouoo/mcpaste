package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/1yoouoo/mcpaste/internal/images"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/1yoouoo/mcpaste/internal/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const workspaceOne = "00000000-0000-4000-8000-000000000101"
const workspaceTwo = "00000000-0000-4000-8000-000000000102"
const deviceOne = "00000000-0000-4000-8000-000000000201"

var cleanupTestApplicationCounter atomic.Uint64

type pausingSnapshotTx struct {
	pgx.Tx
	cursorRead chan<- struct{}
	resume     <-chan struct{}
}

func (tx *pausingSnapshotTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	row := tx.Tx.QueryRow(ctx, sql, args...)
	if !strings.Contains(strings.ToLower(sql), "select next_event_sequence from workspaces") {
		return row
	}
	return &pausingSnapshotRow{Row: row, ctx: ctx, cursorRead: tx.cursorRead, resume: tx.resume}
}

type pausingSnapshotRow struct {
	pgx.Row
	ctx        context.Context
	cursorRead chan<- struct{}
	resume     <-chan struct{}
}

func (row *pausingSnapshotRow) Scan(dest ...any) error {
	if err := row.Row.Scan(dest...); err != nil {
		return err
	}
	select {
	case row.cursorRead <- struct{}{}:
	case <-row.ctx.Done():
		return row.ctx.Err()
	}
	select {
	case <-row.resume:
		return nil
	case <-row.ctx.Done():
		return row.ctx.Err()
	}
}

func TestAppendTextRevisionRejectsContentAfterTombstoneWithoutAdvancingWorkspace(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	createdAt := time.Date(2026, 8, 14, 6, 0, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000001101"
	pasteID := "00000000-0000-4000-8000-000000001102"
	contentRevisionID := "00000000-0000-4000-8000-000000001103"
	tombstoneRevisionID := "00000000-0000-4000-8000-000000001104"
	staleRevisionID := "00000000-0000-4000-8000-000000001105"

	if err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, createdAt); err != nil {
			return err
		}
		if err := tx.InsertPaste(ctx, workspaceID, pasteID, createdAt); err != nil {
			return err
		}
		_, err := tx.AppendTextRevision(
			ctx, workspaceID, pasteID, contentRevisionID,
			identity.RevisionContent, "paste.created", aggregateEnvelope(0x71),
			createdAt, createdAt.AddDate(1, 0, 0),
		)
		return err
	}); err != nil {
		t.Fatalf("seed text paste: %v", err)
	}

	tombstoneAt := createdAt.Add(time.Minute)
	if err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		_, err := tx.AppendTextRevision(
			ctx, workspaceID, pasteID, tombstoneRevisionID,
			identity.RevisionTombstone, "paste.deleted", secure.Envelope{},
			tombstoneAt, tombstoneAt.AddDate(1, 0, 0),
		)
		return err
	}); err != nil {
		t.Fatalf("tombstone text paste: %v", err)
	}

	var staleRevision identity.TextRevision
	var appendErr error
	updateAt := tombstoneAt.Add(time.Minute)
	if err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		staleRevision, appendErr = tx.AppendTextRevision(
			ctx, workspaceID, pasteID, staleRevisionID,
			identity.RevisionContent, "paste.revised", aggregateEnvelope(0x72),
			updateAt, updateAt.AddDate(1, 0, 0),
		)
		return nil
	}); err != nil {
		t.Fatalf("commit rejected stale update transaction: %v", err)
	}
	if !errors.Is(appendErr, identity.ErrNotFound) {
		t.Errorf("AppendTextRevision() after tombstone error = %v, want %v", appendErr, identity.ErrNotFound)
	}
	if staleRevision.RevisionID != "" || staleRevision.ServerSequence != 0 {
		t.Errorf("AppendTextRevision() after tombstone revision = %#v, want zero value", staleRevision)
	}

	var sequence int64
	var revisionCount, contentRevisionCount, staleRevisionCount, revisedEventCount int
	if err := pool.QueryRow(ctx, `
select w.next_event_sequence,
       (select count(*) from paste_revisions r
        where r.workspace_id = w.id and r.paste_id = $2::uuid),
       (select count(*) from paste_revisions r
        where r.workspace_id = w.id and r.paste_id = $2::uuid and r.revision_kind = $3),
       (select count(*) from paste_revisions r
        where r.workspace_id = w.id and r.paste_id = $2::uuid and r.id = $4::uuid),
       (select count(*) from workspace_events e
        where e.workspace_id = w.id and e.object_id = $2::uuid and e.event_type = 'paste.revised')
from workspaces w
where w.id = $1::uuid`, workspaceID, pasteID, identity.RevisionContent, staleRevisionID).Scan(
		&sequence, &revisionCount, &contentRevisionCount, &staleRevisionCount, &revisedEventCount,
	); err != nil {
		t.Fatalf("inspect post-tombstone update state: %v", err)
	}
	if sequence != 2 || revisionCount != 2 || contentRevisionCount != 1 || staleRevisionCount != 0 || revisedEventCount != 0 {
		t.Errorf(
			"post-tombstone update state = sequence:%d revisions:%d content:%d stale:%d revised-events:%d, want 2/2/1/0/0",
			sequence, revisionCount, contentRevisionCount, staleRevisionCount, revisedEventCount,
		)
	}

	var aggregate identity.PasteAggregate
	if err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		var err error
		aggregate, err = tx.PasteAggregate(ctx, workspaceID, pasteID, updateAt)
		return err
	}); err != nil {
		t.Fatalf("read aggregate after rejected update: %v", err)
	}
	if !aggregate.Deleted || aggregate.RevisionID != tombstoneRevisionID || aggregate.ServerSequence != 2 || aggregate.TextRevision != nil || aggregate.AttachmentRevision != nil {
		t.Errorf("aggregate after rejected update = %#v, want deleted tombstone at sequence 2", aggregate)
	}
}

func TestAppendTextRevisionSerializesBehindConcurrentTombstone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool := testdb.New(t)
	store := New(pool)
	createdAt := time.Date(2026, 8, 14, 6, 30, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000001111"
	pasteID := "00000000-0000-4000-8000-000000001112"
	if err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, createdAt); err != nil {
			return err
		}
		if err := tx.InsertPaste(ctx, workspaceID, pasteID, createdAt); err != nil {
			return err
		}
		_, err := tx.AppendTextRevision(
			ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000001113",
			identity.RevisionContent, "paste.created", aggregateEnvelope(0x73),
			createdAt, createdAt.AddDate(1, 0, 0),
		)
		return err
	}); err != nil {
		t.Fatalf("seed concurrent text paste: %v", err)
	}

	releaseTombstone := make(chan struct{})
	tombstoneLocked := make(chan int, 1)
	tombstoneDone := make(chan error, 1)
	go func() {
		err := store.WithinTx(ctx, func(tx identity.TxStore) error {
			postgresTx := tx.(*txStore)
			var backendPID int
			if err := postgresTx.tx.QueryRow(ctx, `select pg_backend_pid()`).Scan(&backendPID); err != nil {
				return err
			}
			tombstoneAt := createdAt.Add(time.Minute)
			if _, err := tx.AppendTextRevision(
				ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000001114",
				identity.RevisionTombstone, "paste.deleted", secure.Envelope{},
				tombstoneAt, tombstoneAt.AddDate(1, 0, 0),
			); err != nil {
				return err
			}
			tombstoneLocked <- backendPID
			select {
			case <-releaseTombstone:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		tombstoneDone <- err
	}()

	var tombstoneBackendPID int
	select {
	case tombstoneBackendPID = <-tombstoneLocked:
	case err := <-tombstoneDone:
		t.Fatalf("tombstone transaction ended before pause: %v", err)
	case <-ctx.Done():
		t.Fatal("tombstone transaction did not reach commit pause")
	}

	type textOutcome struct {
		revision identity.TextRevision
		err      error
	}
	staleRevisionID := "00000000-0000-4000-8000-000000001115"
	updateStarted := make(chan int, 1)
	updateDone := make(chan textOutcome, 1)
	updateComplete := make(chan struct{})
	go func() {
		var revision identity.TextRevision
		err := store.WithinTx(ctx, func(tx identity.TxStore) error {
			postgresTx := tx.(*txStore)
			var backendPID int
			if err := postgresTx.tx.QueryRow(ctx, `select pg_backend_pid()`).Scan(&backendPID); err != nil {
				return err
			}
			updateStarted <- backendPID
			var err error
			updateAt := createdAt.Add(2 * time.Minute)
			revision, err = tx.AppendTextRevision(
				ctx, workspaceID, pasteID, staleRevisionID,
				identity.RevisionContent, "paste.revised", aggregateEnvelope(0x74),
				updateAt, updateAt.AddDate(1, 0, 0),
			)
			return err
		})
		updateDone <- textOutcome{revision: revision, err: err}
		close(updateComplete)
	}()

	var updateBackendPID int
	select {
	case updateBackendPID = <-updateStarted:
	case <-ctx.Done():
		t.Fatal("concurrent stale update did not start")
	}
	waitForBackendBlockedBy(t, ctx, pool, updateBackendPID, tombstoneBackendPID, updateComplete)
	close(releaseTombstone)
	if err := <-tombstoneDone; err != nil {
		t.Fatalf("commit tombstone transaction: %v", err)
	}
	update := <-updateDone
	if !errors.Is(update.err, identity.ErrNotFound) || update.revision.RevisionID != "" || update.revision.ServerSequence != 0 {
		t.Errorf("concurrent update after tombstone = %#v, %v", update.revision, update.err)
	}

	var sequence int64
	var contentRevisionCount, staleRevisionCount, revisedEventCount int
	var latestKind string
	if err := pool.QueryRow(ctx, `
select w.next_event_sequence,
       (select count(*) from paste_revisions r
        where r.workspace_id = w.id and r.paste_id = $2::uuid and r.revision_kind = $3),
       (select count(*) from paste_revisions r
        where r.workspace_id = w.id and r.paste_id = $2::uuid and r.id = $4::uuid),
       (select count(*) from workspace_events e
        where e.workspace_id = w.id and e.object_id = $2::uuid and e.event_type = 'paste.revised'),
       (select r.revision_kind from paste_revisions r
        where r.workspace_id = w.id and r.paste_id = $2::uuid
        order by r.server_sequence desc limit 1)
from workspaces w
where w.id = $1::uuid`, workspaceID, pasteID, identity.RevisionContent, staleRevisionID).Scan(
		&sequence, &contentRevisionCount, &staleRevisionCount, &revisedEventCount, &latestKind,
	); err != nil {
		t.Fatalf("inspect concurrent tombstone result: %v", err)
	}
	if sequence != 2 || contentRevisionCount != 1 || staleRevisionCount != 0 || revisedEventCount != 0 || latestKind != identity.RevisionTombstone {
		t.Errorf(
			"concurrent tombstone state = sequence:%d content:%d stale:%d revised-events:%d latest:%q, want 2/1/0/0/%q",
			sequence, contentRevisionCount, staleRevisionCount, revisedEventCount, latestKind, identity.RevisionTombstone,
		)
	}
}

func TestAppendAttachmentRevisionPersistsOrderedAssetsAndExplicitClear(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	createdAt := time.Date(2026, 8, 14, 7, 0, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000961"
	pasteID := "00000000-0000-4000-8000-000000000962"
	attachmentID := "00000000-0000-4000-8000-000000000964"
	clearID := "00000000-0000-4000-8000-000000000965"
	attachmentAt := createdAt.Add(time.Minute)
	assets := []identity.ImageAsset{
		aggregateAsset(1, "append/one", attachmentAt.Add(identity.ImageLifetime)),
		aggregateAsset(0, "append/zero", attachmentAt.Add(identity.ImageLifetime)),
	}
	assets[0].Envelope.Ciphertext = []byte{0x31}
	assets[0].Bytes = []byte{0x41}
	assets[1].Envelope.Ciphertext = []byte{0x30}
	assets[1].Bytes = []byte{0x40}

	var attachment, clear identity.TextRevision
	var ordered, cleared []identity.ImageAsset
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, createdAt); err != nil {
			return err
		}
		if err := tx.InsertPaste(ctx, workspaceID, pasteID, createdAt); err != nil {
			return err
		}
		if _, err := tx.AppendTextRevision(
			ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000000963",
			identity.RevisionContent, "paste.created", aggregateEnvelope(0x81),
			createdAt, createdAt.AddDate(1, 0, 0),
		); err != nil {
			return err
		}
		var err error
		attachment, err = tx.AppendAttachmentRevision(
			ctx, workspaceID, pasteID, attachmentID, "paste.revised", assets,
			attachmentAt, attachmentAt.Add(identity.ImageLifetime),
		)
		if err != nil {
			return err
		}
		for index := range assets {
			assets[index].StorageKey = "mutated-after-append"
			assets[index].Envelope.Nonce[0] = 0xff
			assets[index].Envelope.Ciphertext[0] = 0xff
			assets[index].Bytes[0] = 0xff
		}
		ordered, err = tx.ListImageAssets(ctx, workspaceID, pasteID, attachmentID)
		if err != nil {
			return err
		}
		clearAt := createdAt.Add(2 * time.Minute)
		clear, err = tx.AppendAttachmentRevision(
			ctx, workspaceID, pasteID, clearID, "paste.revised", nil,
			clearAt, clearAt.Add(identity.ImageLifetime),
		)
		if err != nil {
			return err
		}
		cleared, err = tx.ListImageAssets(ctx, workspaceID, pasteID, clearID)
		return err
	})
	if err != nil {
		t.Fatalf("append attachment transaction: %v", err)
	}
	if attachment.RevisionKind != identity.RevisionAttachmentBundle || attachment.ServerSequence != 2 || len(attachment.Assets) != 2 {
		t.Fatalf("AppendAttachmentRevision() = %#v", attachment)
	}
	if first := attachment.Assets[0]; first.AssetIndex != 0 || first.StorageKey != "append/zero" || first.Envelope.Nonce[0] != 0x01 || !bytes.Equal(first.Envelope.Ciphertext, []byte{0x30}) || !bytes.Equal(first.Bytes, []byte{0x40}) {
		t.Fatalf("first copied attachment asset = %#v", first)
	}
	if second := attachment.Assets[1]; second.AssetIndex != 1 || second.StorageKey != "append/one" || second.Envelope.Nonce[0] != 0x02 || !bytes.Equal(second.Envelope.Ciphertext, []byte{0x31}) || !bytes.Equal(second.Bytes, []byte{0x41}) {
		t.Fatalf("second copied attachment asset = %#v", second)
	}
	if len(ordered) != 2 || ordered[0].AssetIndex != 0 || ordered[1].AssetIndex != 1 {
		t.Fatalf("ordered attachment assets = %#v", ordered)
	}
	if clear.RevisionKind != identity.RevisionAttachmentBundle || clear.ServerSequence != 3 || clear.Assets == nil || len(clear.Assets) != 0 || len(cleared) != 0 {
		t.Fatalf("explicit clear revision/assets = %#v / %#v", clear, cleared)
	}
	var pasteKind string
	var sequence int64
	var attachmentRevisions, attachmentAssets, attachmentEvents int
	if err := pool.QueryRow(ctx, `
select p.paste_kind, w.next_event_sequence,
       (select count(*) from paste_revisions r where r.workspace_id = p.workspace_id and r.paste_id = p.id and r.revision_kind = $3),
       (select count(*) from paste_assets a where a.workspace_id = p.workspace_id and a.paste_id = p.id),
       (select count(*) from workspace_events e where e.workspace_id = p.workspace_id and e.object_id = p.id and e.event_type = 'paste.revised')
from pastes p
join workspaces w on w.id = p.workspace_id
where p.workspace_id = $1::uuid and p.id = $2::uuid`, workspaceID, pasteID, identity.RevisionAttachmentBundle).Scan(
		&pasteKind, &sequence, &attachmentRevisions, &attachmentAssets, &attachmentEvents,
	); err != nil {
		t.Fatalf("inspect attachment persistence: %v", err)
	}
	if pasteKind != "text" || sequence != 3 || attachmentRevisions != 2 || attachmentAssets != 2 || attachmentEvents != 2 {
		t.Fatalf("attachment persistence = kind:%s sequence:%d revisions:%d assets:%d events:%d", pasteKind, sequence, attachmentRevisions, attachmentAssets, attachmentEvents)
	}
}

func TestAppendAttachmentRevisionRejectsPasteWithoutRevisionBeforeSequenceAllocation(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	createdAt := time.Date(2026, 8, 14, 7, 30, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000966"
	pasteID := "00000000-0000-4000-8000-000000000967"

	var appendErr error
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, createdAt); err != nil {
			return err
		}
		if err := tx.InsertPaste(ctx, workspaceID, pasteID, createdAt); err != nil {
			return err
		}
		_, appendErr = tx.AppendAttachmentRevision(
			ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000000968", "paste.revised", nil,
			createdAt, createdAt.Add(identity.ImageLifetime),
		)
		return nil
	})
	if err != nil {
		t.Fatalf("bare paste attachment transaction: %v", err)
	}
	if !errors.Is(appendErr, identity.ErrNotFound) {
		t.Fatalf("bare paste AppendAttachmentRevision() error = %v", appendErr)
	}
	var sequence int64
	if err := pool.QueryRow(ctx, `select next_event_sequence from workspaces where id = $1::uuid`, workspaceID).Scan(&sequence); err != nil {
		t.Fatalf("read bare paste sequence: %v", err)
	}
	if sequence != 0 {
		t.Fatalf("next_event_sequence after bare paste rejection = %d, want 0", sequence)
	}
}

func TestAppendAttachmentRevisionRejectsInvalidModernIndexesBeforeSequenceAllocation(t *testing.T) {
	tests := []struct {
		name    string
		indexes []int
	}{
		{name: "sparse", indexes: []int{1}},
		{name: "duplicate", indexes: []int{0, 0}},
		{name: "negative", indexes: []int{-1}},
		{name: "legacy-only", indexes: []int{8}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			pool := testdb.New(t)
			store := New(pool)
			createdAt := time.Date(2026, 8, 14, 7, 45, 0, 0, time.UTC)
			workspaceID := "00000000-0000-4000-8000-000000000969"
			pasteID := "00000000-0000-4000-8000-000000000970"
			assets := make([]identity.ImageAsset, len(test.indexes))
			for index, assetIndex := range test.indexes {
				assets[index] = aggregateAsset(assetIndex, fmt.Sprintf("append/invalid/%d", index), createdAt.Add(identity.ImageLifetime))
			}

			var appendErr error
			err := store.WithinTx(ctx, func(tx identity.TxStore) error {
				if err := tx.InsertWorkspace(ctx, workspaceID, createdAt); err != nil {
					return err
				}
				if err := tx.InsertPaste(ctx, workspaceID, pasteID, createdAt); err != nil {
					return err
				}
				if _, err := tx.AppendTextRevision(
					ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000000971",
					identity.RevisionContent, "paste.created", aggregateEnvelope(0x82),
					createdAt, createdAt.AddDate(1, 0, 0),
				); err != nil {
					return err
				}
				_, appendErr = tx.AppendAttachmentRevision(
					ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000000972", "paste.revised", assets,
					createdAt.Add(time.Minute), createdAt.Add(time.Minute).Add(identity.ImageLifetime),
				)
				return nil
			})
			if err != nil {
				t.Fatalf("invalid attachment index transaction: %v", err)
			}
			if !errors.Is(appendErr, identity.ErrInvalid) {
				t.Fatalf("AppendAttachmentRevision() indexes %v error = %v", test.indexes, appendErr)
			}
			var sequence int64
			if err := pool.QueryRow(ctx, `select next_event_sequence from workspaces where id = $1::uuid`, workspaceID).Scan(&sequence); err != nil {
				t.Fatalf("read invalid attachment index sequence: %v", err)
			}
			if sequence != 1 {
				t.Fatalf("next_event_sequence after indexes %v = %d, want 1", test.indexes, sequence)
			}
		})
	}
}

func TestAppendAttachmentRevisionValidatesTargetCountAndEvent(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	createdAt := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000971"
	pasteID := "00000000-0000-4000-8000-000000000972"
	overLimit := make([]identity.ImageAsset, images.MaxAttachmentItems+1)
	for index := range overLimit {
		overLimit[index] = aggregateAsset(index, fmt.Sprintf("append/limit/%d", index), createdAt.Add(identity.ImageLifetime))
	}

	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, createdAt); err != nil {
			return err
		}
		if err := tx.InsertPaste(ctx, workspaceID, pasteID, createdAt); err != nil {
			return err
		}
		if _, err := tx.AppendTextRevision(
			ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000000973",
			identity.RevisionContent, "paste.created", aggregateEnvelope(0x91),
			createdAt, createdAt.AddDate(1, 0, 0),
		); err != nil {
			return err
		}
		if _, err := tx.AppendAttachmentRevision(
			ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000000974", "paste.created", nil,
			createdAt, createdAt.Add(identity.ImageLifetime),
		); !errors.Is(err, identity.ErrInvalid) {
			t.Fatalf("paste.created attachment error = %v", err)
		}
		if _, err := tx.AppendAttachmentRevision(
			ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000000975", "paste.revised", overLimit,
			createdAt, createdAt.Add(identity.ImageLifetime),
		); !errors.Is(err, identity.ErrInvalid) {
			t.Fatalf("over-limit attachment error = %v", err)
		}
		if _, err := tx.AppendAttachmentRevision(
			ctx, workspaceID, "00000000-0000-4000-8000-000000000979", "00000000-0000-4000-8000-000000000976", "paste.revised", nil,
			createdAt, createdAt.Add(identity.ImageLifetime),
		); !errors.Is(err, identity.ErrNotFound) {
			t.Fatalf("missing paste attachment error = %v", err)
		}
		tombstoneAt := createdAt.Add(time.Minute)
		if _, err := tx.AppendTextRevision(
			ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000000977",
			identity.RevisionTombstone, "paste.deleted", secure.Envelope{},
			tombstoneAt, tombstoneAt.AddDate(1, 0, 0),
		); err != nil {
			return err
		}
		if _, err := tx.AppendAttachmentRevision(
			ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000000978", "paste.revised", nil,
			tombstoneAt, tombstoneAt.Add(identity.ImageLifetime),
		); !errors.Is(err, identity.ErrNotFound) {
			t.Fatalf("tombstoned paste attachment error = %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("attachment validation transaction: %v", err)
	}
	var sequence int64
	if err := pool.QueryRow(ctx, `select next_event_sequence from workspaces where id = $1::uuid`, workspaceID).Scan(&sequence); err != nil {
		t.Fatalf("read attachment validation sequence: %v", err)
	}
	if sequence != 2 {
		t.Fatalf("next_event_sequence after rejected attachments = %d, want 2", sequence)
	}
}

func TestAppendAttachmentRevisionSerializesBehindConcurrentTombstone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool := testdb.New(t)
	store := New(pool)
	createdAt := time.Date(2026, 8, 14, 8, 30, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000973"
	pasteID := "00000000-0000-4000-8000-000000000974"
	if err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, createdAt); err != nil {
			return err
		}
		if err := tx.InsertPaste(ctx, workspaceID, pasteID, createdAt); err != nil {
			return err
		}
		_, err := tx.AppendTextRevision(
			ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000000975",
			identity.RevisionContent, "paste.created", aggregateEnvelope(0x92),
			createdAt, createdAt.AddDate(1, 0, 0),
		)
		return err
	}); err != nil {
		t.Fatalf("seed tombstone race: %v", err)
	}

	releaseTombstone := make(chan struct{})
	tombstoneLocked := make(chan int, 1)
	tombstoneDone := make(chan error, 1)
	go func() {
		err := store.WithinTx(ctx, func(tx identity.TxStore) error {
			postgresTx := tx.(*txStore)
			var backendPID int
			if err := postgresTx.tx.QueryRow(ctx, `select pg_backend_pid()`).Scan(&backendPID); err != nil {
				return err
			}
			tombstoneAt := createdAt.Add(time.Minute)
			if _, err := tx.AppendTextRevision(
				ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000000976",
				identity.RevisionTombstone, "paste.deleted", secure.Envelope{},
				tombstoneAt, tombstoneAt.AddDate(1, 0, 0),
			); err != nil {
				return err
			}
			tombstoneLocked <- backendPID
			select {
			case <-releaseTombstone:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		tombstoneDone <- err
	}()

	var tombstoneBackendPID int
	select {
	case tombstoneBackendPID = <-tombstoneLocked:
	case err := <-tombstoneDone:
		t.Fatalf("tombstone transaction ended before pause: %v", err)
	case <-ctx.Done():
		t.Fatal("tombstone transaction did not reach commit pause")
	}

	type attachmentOutcome struct {
		revision identity.TextRevision
		err      error
	}
	attachmentStarted := make(chan int, 1)
	attachmentDone := make(chan attachmentOutcome, 1)
	attachmentComplete := make(chan struct{})
	go func() {
		var revision identity.TextRevision
		err := store.WithinTx(ctx, func(tx identity.TxStore) error {
			postgresTx := tx.(*txStore)
			var backendPID int
			if err := postgresTx.tx.QueryRow(ctx, `select pg_backend_pid()`).Scan(&backendPID); err != nil {
				return err
			}
			attachmentStarted <- backendPID
			var err error
			revision, err = tx.AppendAttachmentRevision(
				ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000000977", "paste.revised", nil,
				createdAt.Add(2*time.Minute), createdAt.Add(2*time.Minute).Add(identity.ImageLifetime),
			)
			return err
		})
		attachmentDone <- attachmentOutcome{revision: revision, err: err}
		close(attachmentComplete)
	}()

	var attachmentBackendPID int
	select {
	case attachmentBackendPID = <-attachmentStarted:
	case <-ctx.Done():
		t.Fatal("attachment transaction did not start")
	}
	waitForBackendBlockedBy(t, ctx, pool, attachmentBackendPID, tombstoneBackendPID, attachmentComplete)
	close(releaseTombstone)
	if err := <-tombstoneDone; err != nil {
		t.Fatalf("commit tombstone transaction: %v", err)
	}
	attachment := <-attachmentDone
	if !errors.Is(attachment.err, identity.ErrNotFound) || attachment.revision.RevisionID != "" || attachment.revision.ServerSequence != 0 {
		t.Fatalf("attachment after tombstone = %#v, %v", attachment.revision, attachment.err)
	}

	var sequence int64
	var attachmentRevisions, attachmentEvents int
	if err := pool.QueryRow(ctx, `
select w.next_event_sequence,
       (select count(*) from paste_revisions r
        where r.workspace_id = w.id and r.paste_id = $2::uuid and r.revision_kind = $3),
       (select count(*) from workspace_events e
        where e.workspace_id = w.id and e.object_id = $2::uuid and e.event_type = 'paste.revised')
from workspaces w
where w.id = $1::uuid`, workspaceID, pasteID, identity.RevisionAttachmentBundle).Scan(
		&sequence, &attachmentRevisions, &attachmentEvents,
	); err != nil {
		t.Fatalf("inspect tombstone race result: %v", err)
	}
	if sequence != 2 || attachmentRevisions != 0 || attachmentEvents != 0 {
		t.Fatalf("tombstone race state = sequence:%d revisions:%d events:%d", sequence, attachmentRevisions, attachmentEvents)
	}
}

func TestPasteAggregateSnapshotKeepsCursorAndContentsAtOneBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool := testdb.New(t)
	store := New(pool)
	createdAt := time.Date(2026, 8, 14, 8, 45, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000978"
	pasteID := "00000000-0000-4000-8000-000000000979"
	firstRevisionID := "00000000-0000-4000-8000-000000000980"
	if err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, createdAt); err != nil {
			return err
		}
		if err := tx.InsertPaste(ctx, workspaceID, pasteID, createdAt); err != nil {
			return err
		}
		_, err := tx.AppendTextRevision(
			ctx, workspaceID, pasteID, firstRevisionID,
			identity.RevisionContent, "paste.created", aggregateEnvelope(0x93),
			createdAt, createdAt.AddDate(1, 0, 0),
		)
		return err
	}); err != nil {
		t.Fatalf("seed snapshot boundary: %v", err)
	}

	type snapshotOutcome struct {
		cursor     int64
		aggregates []identity.PasteAggregate
		err        error
	}
	snapshotStarted := make(chan int, 1)
	cursorRead := make(chan struct{})
	resumeSnapshot := make(chan struct{})
	snapshotDone := make(chan snapshotOutcome, 1)
	go func() {
		var outcome snapshotOutcome
		outcome.err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var backendPID int
			if err := tx.QueryRow(ctx, `select pg_backend_pid()`).Scan(&backendPID); err != nil {
				return err
			}
			snapshotStarted <- backendPID
			store := &txStore{tx: &pausingSnapshotTx{Tx: tx, cursorRead: cursorRead, resume: resumeSnapshot}}
			var err error
			outcome.cursor, outcome.aggregates, err = store.SnapshotAggregates(ctx, workspaceID, createdAt.Add(3*time.Minute))
			return err
		})
		snapshotDone <- outcome
	}()

	var snapshotBackendPID int
	select {
	case snapshotBackendPID = <-snapshotStarted:
	case <-ctx.Done():
		t.Fatal("snapshot transaction did not start")
	}
	select {
	case <-cursorRead:
	case <-ctx.Done():
		t.Fatal("snapshot did not pause after reading cursor")
	}

	type textOutcome struct {
		revision identity.TextRevision
		err      error
	}
	appendStarted := make(chan int, 1)
	appendDone := make(chan textOutcome, 1)
	appendComplete := make(chan struct{})
	go func() {
		var revision identity.TextRevision
		err := store.WithinTx(ctx, func(tx identity.TxStore) error {
			postgresTx := tx.(*txStore)
			var backendPID int
			if err := postgresTx.tx.QueryRow(ctx, `select pg_backend_pid()`).Scan(&backendPID); err != nil {
				return err
			}
			appendStarted <- backendPID
			var err error
			revision, err = tx.AppendTextRevision(
				ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000000981",
				identity.RevisionContent, "paste.revised", aggregateEnvelope(0x94),
				createdAt.Add(time.Minute), createdAt.Add(time.Minute).AddDate(1, 0, 0),
			)
			return err
		})
		appendDone <- textOutcome{revision: revision, err: err}
		close(appendComplete)
	}()

	var appendBackendPID int
	select {
	case appendBackendPID = <-appendStarted:
	case <-ctx.Done():
		t.Fatal("concurrent snapshot append did not start")
	}
	waitForBackendBlockedBy(t, ctx, pool, appendBackendPID, snapshotBackendPID, appendComplete)
	close(resumeSnapshot)
	snapshot := <-snapshotDone
	if snapshot.err != nil {
		t.Fatalf("SnapshotAggregates() error = %v", snapshot.err)
	}
	if snapshot.cursor != 1 || len(snapshot.aggregates) != 1 || snapshot.aggregates[0].RevisionID != firstRevisionID || snapshot.aggregates[0].ServerSequence != 1 {
		t.Fatalf("snapshot boundary = cursor:%d aggregates:%#v", snapshot.cursor, snapshot.aggregates)
	}
	appendResult := <-appendDone
	if appendResult.err != nil || appendResult.revision.ServerSequence != 2 {
		t.Fatalf("append after snapshot = %#v, %v", appendResult.revision, appendResult.err)
	}
}

func TestSyncStreamsUnexpiredAttachmentKindsAndOmitsExpiredImageComponents(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000981"

	var result identity.SyncResult
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, now.Add(-time.Hour)); err != nil {
			return err
		}
		textPasteID := "00000000-0000-4000-8000-000000000982"
		if err := tx.InsertPaste(ctx, workspaceID, textPasteID, now.Add(-time.Hour)); err != nil {
			return err
		}
		if _, err := tx.AppendTextRevision(
			ctx, workspaceID, textPasteID, "00000000-0000-4000-8000-000000000983",
			identity.RevisionContent, "paste.created", aggregateEnvelope(0xa1),
			now.Add(-time.Hour), now.Add(-time.Hour).AddDate(1, 0, 0),
		); err != nil {
			return err
		}
		attachmentAt := now.Add(-30 * time.Minute)
		if _, err := tx.AppendAttachmentRevision(
			ctx, workspaceID, textPasteID, "00000000-0000-4000-8000-000000000984", "paste.revised",
			[]identity.ImageAsset{aggregateAsset(0, "sync/attachment", attachmentAt.Add(identity.ImageLifetime))},
			attachmentAt, attachmentAt.Add(identity.ImageLifetime),
		); err != nil {
			return err
		}

		legacyPasteID := "00000000-0000-4000-8000-000000000985"
		if err := tx.InsertPaste(ctx, workspaceID, legacyPasteID, attachmentAt); err != nil {
			return err
		}
		if _, err := tx.AppendImageRevision(
			ctx, workspaceID, legacyPasteID, "00000000-0000-4000-8000-000000000986", "paste.created",
			[]identity.ImageAsset{aggregateAsset(0, "sync/legacy", attachmentAt.Add(identity.ImageLifetime))},
			attachmentAt, attachmentAt.Add(identity.ImageLifetime),
		); err != nil {
			return err
		}

		expiredAt := now.Add(-25 * time.Hour)
		expiredAttachmentPasteID := "00000000-0000-4000-8000-000000000987"
		if err := tx.InsertPaste(ctx, workspaceID, expiredAttachmentPasteID, expiredAt); err != nil {
			return err
		}
		if _, err := tx.AppendTextRevision(
			ctx, workspaceID, expiredAttachmentPasteID, "00000000-0000-4000-8000-000000000991",
			identity.RevisionContent, "paste.created", aggregateEnvelope(0xa2),
			expiredAt, expiredAt.AddDate(1, 0, 0),
		); err != nil {
			return err
		}
		if _, err := tx.AppendAttachmentRevision(
			ctx, workspaceID, expiredAttachmentPasteID, "00000000-0000-4000-8000-000000000988", "paste.revised", nil,
			expiredAt, expiredAt.Add(identity.ImageLifetime),
		); err != nil {
			return err
		}

		expiredLegacyPasteID := "00000000-0000-4000-8000-000000000989"
		if err := tx.InsertPaste(ctx, workspaceID, expiredLegacyPasteID, expiredAt); err != nil {
			return err
		}
		if _, err := tx.AppendImageRevision(
			ctx, workspaceID, expiredLegacyPasteID, "00000000-0000-4000-8000-000000000990", "paste.created",
			[]identity.ImageAsset{aggregateAsset(0, "sync/expired-legacy", expiredAt.Add(identity.ImageLifetime))},
			expiredAt, expiredAt.Add(identity.ImageLifetime),
		); err != nil {
			return err
		}

		var err error
		result, err = tx.Sync(ctx, workspaceID, 1, 10, now)
		return err
	})
	if err != nil {
		t.Fatalf("sync attachment transaction: %v", err)
	}
	if result.Cursor != 6 || result.HasMore || len(result.Events) != 3 {
		t.Fatalf("attachment sync result = %#v", result)
	}
	if result.Events[0].Sequence != 2 || result.Events[0].RevisionKind != identity.RevisionAttachmentBundle ||
		result.Events[1].Sequence != 3 || result.Events[1].RevisionKind != identity.RevisionImageBundle ||
		result.Events[2].Sequence != 4 || result.Events[2].RevisionKind != identity.RevisionContent {
		t.Fatalf("attachment sync events = %#v", result.Events)
	}
}

func TestWorkspaceScopedCredentialAuthentication(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	hash := bytes.Repeat([]byte{0x41}, 32)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		device, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now,
		})
		if err != nil || device.DisplayName != "MacBook Pro" {
			t.Fatalf("InsertDevice() = %#v, %v", device, err)
		}
		return tx.InsertCredential(ctx, workspaceOne, identity.CredentialRecord{
			DeviceID: deviceOne, Locator: "AAAAAAAAAAAAAAAAAAAAAA", Scope: "full", Hash: hash, CreatedAt: now,
		})
	})
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	principal, err := store.Authenticate(ctx, workspaceOne, "AAAAAAAAAAAAAAAAAAAAAA", hash, now)
	if err != nil || principal.WorkspaceID != workspaceOne || principal.DeviceID != deviceOne || principal.Scope != "full" {
		t.Fatalf("Authenticate() = %#v, %v", principal, err)
	}
	if _, err := store.Authenticate(ctx, workspaceTwo, "AAAAAAAAAAAAAAAAAAAAAA", hash, now); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("cross-workspace Authenticate() error = %v", err)
	}
	if _, err := store.Authenticate(ctx, workspaceOne, "AAAAAAAAAAAAAAAAAAAAAA", bytes.Repeat([]byte{0x42}, 32), now); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("wrong-secret Authenticate() error = %v", err)
	}
}

func TestAuthenticateRejectsCredentialRevokedBeforeLastUsedUpdate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool := testdb.New(t)
	store := New(pool)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	hash := bytes.Repeat([]byte{0x43}, 32)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		if _, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "Race Mac", Platform: "macos", Role: "full", CreatedAt: now,
		}); err != nil {
			return err
		}
		return tx.InsertCredential(ctx, workspaceOne, identity.CredentialRecord{
			DeviceID: deviceOne, Locator: "BBBBBBBBBBBBBBBBBBBBBB", Scope: "full", Hash: hash, CreatedAt: now,
		})
	})
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin credential lock: %v", err)
	}
	defer func() { _ = lockTx.Rollback(context.Background()) }()
	var lockBackendPID int
	if err := lockTx.QueryRow(ctx, "select pg_backend_pid()").Scan(&lockBackendPID); err != nil {
		t.Fatalf("read credential lock backend PID: %v", err)
	}
	if _, err := lockTx.Exec(ctx, `
select 1 from credentials
where workspace_id = $1::uuid and token_id = $2
for update`, workspaceOne, "BBBBBBBBBBBBBBBBBBBBBB"); err != nil {
		t.Fatalf("lock credential: %v", err)
	}

	type authenticationResult struct {
		principal identity.Principal
		err       error
	}
	result := make(chan authenticationResult, 1)
	go func() {
		principal, err := store.Authenticate(ctx, workspaceOne, "BBBBBBBBBBBBBBBBBBBBBB", hash, now.Add(time.Minute))
		result <- authenticationResult{principal: principal, err: err}
	}()
	waitForBlockedLastUsedUpdate(t, ctx, pool, lockBackendPID)

	if _, err := lockTx.Exec(ctx, `
update credentials set revoked_at = $3
where workspace_id = $1::uuid and token_id = $2`, workspaceOne, "BBBBBBBBBBBBBBBBBBBBBB", now); err != nil {
		t.Fatalf("revoke credential: %v", err)
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("commit credential revocation: %v", err)
	}

	got := <-result
	if !errors.Is(got.err, identity.ErrUnauthorized) {
		t.Fatalf("Authenticate() error = %v", got.err)
	}
	if got.principal != (identity.Principal{}) {
		t.Fatalf("Authenticate() principal = %#v", got.principal)
	}
}

func waitForBlockedLastUsedUpdate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, lockBackendPID int) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
select exists(
    select 1
    from pg_stat_activity waiting_auth
    where waiting_auth.datname = current_database()
      and waiting_auth.pid <> pg_backend_pid()
      and waiting_auth.wait_event_type = 'Lock'
      and position('update credentials set last_used_at' in waiting_auth.query) > 0
      and $1::integer = any(pg_blocking_pids(waiting_auth.pid))
)`, lockBackendPID).Scan(&waiting); err != nil {
			t.Fatalf("inspect blocked authentication update: %v", err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("authentication update did not block on credential row")
		case <-ticker.C:
		}
	}
}

func waitForBackendBlockedBy(t *testing.T, ctx context.Context, pool *pgxpool.Pool, waitingBackendPID, blockingBackendPID int, completed <-chan struct{}) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
select exists(
    select 1
    from pg_stat_activity waiting_transaction
    where waiting_transaction.datname = current_database()
      and waiting_transaction.pid = $1
      and waiting_transaction.wait_event_type = 'Lock'
      and $2::integer = any(pg_blocking_pids(waiting_transaction.pid))
)`, waitingBackendPID, blockingBackendPID).Scan(&waiting); err != nil {
			t.Fatalf("inspect blocked transaction: %v", err)
		}
		if waiting {
			return
		}
		select {
		case <-completed:
			t.Fatal("transaction completed before acquiring the expected workspace lock")
		case <-ctx.Done():
			t.Fatal("transaction did not block on the expected workspace lock")
		case <-ticker.C:
		}
	}
}

func TestDeviceNameSuffixIsWorkspaceLocalAndCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		if err := tx.InsertWorkspace(ctx, workspaceTwo, now); err != nil {
			return err
		}
		first, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: deviceOne, DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil || first.DisplayName != "MacBook Pro" {
			t.Fatalf("first device = %#v, %v", first, err)
		}
		second, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: "00000000-0000-4000-8000-000000000202", DisplayName: "macbook pro", Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil || second.DisplayName != "macbook pro (2)" {
			t.Fatalf("second device = %#v, %v", second, err)
		}
		other, err := tx.InsertDevice(ctx, workspaceTwo, identity.Device{ID: "00000000-0000-4000-8000-000000000203", DisplayName: "MACBOOK PRO", Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil || other.DisplayName != "MACBOOK PRO" {
			t.Fatalf("other workspace device = %#v, %v", other, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("device suffix transaction: %v", err)
	}
}

func TestDeviceNameSuffixUsesUnicodeCaseFolding(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		first, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "Straße", Platform: "macos", Role: "full", CreatedAt: now,
		})
		if err != nil || first.DisplayName != "Straße" {
			t.Fatalf("first device = %#v, %v", first, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed Unicode device: %v", err)
	}
	if _, err := pool.Exec(ctx, `
update devices set revoked_at = $2
where workspace_id = $1::uuid`, workspaceOne, now); err != nil {
		t.Fatalf("revoke Unicode device: %v", err)
	}

	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		second, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: "00000000-0000-4000-8000-000000000204", DisplayName: "STRASSE", Platform: "macos", Role: "full", CreatedAt: now,
		})
		if err != nil || second.DisplayName != "STRASSE (2)" {
			t.Fatalf("second device = %#v, %v", second, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Unicode case-fold insertion: %v", err)
	}
}

func TestDeviceNameSuffixUsesPostgreSQLLower(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		first, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "İ", Platform: "macos", Role: "full", CreatedAt: now,
		})
		if err != nil || first.DisplayName != "İ" {
			t.Fatalf("first device = %#v, %v", first, err)
		}
		second, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: "00000000-0000-4000-8000-000000000205", DisplayName: "i", Platform: "macos", Role: "full", CreatedAt: now,
		})
		if err != nil || second.DisplayName != "i (2)" {
			t.Fatalf("second device = %#v, %v", second, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("PostgreSQL lower insertion: %v", err)
	}
}

func TestIdempotencyAndRateLimitPersistence(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	keyHash := bytes.Repeat([]byte{0x51}, 32)
	requestHash := bytes.Repeat([]byte{0x52}, 32)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		return tx.PutIdempotency(ctx, identity.IdempotencyRecord{
			ScopeID: "public", Operation: "workspace.create", KeyHash: keyHash, RequestHash: requestHash,
			Response: identity.StoredResponse{Status: 201, ContentType: "application/json", Envelope: secure.Envelope{
				KeyID: "test-key", Nonce: bytes.Repeat([]byte{0x53}, 12), Ciphertext: []byte{0x54},
			}},
		})
	})
	if err != nil {
		t.Fatalf("PutIdempotency() error = %v", err)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		got, err := tx.GetIdempotency(ctx, "public", "workspace.create", keyHash)
		if err != nil || !bytes.Equal(got.RequestHash, requestHash) || got.Response.Status != 201 {
			t.Fatalf("GetIdempotency() metadata mismatch: err=%v status=%d", err, got.Response.Status)
		}
		if got.ScopeID != "public" || got.Expired || got.ExpiresAt.Sub(got.CreatedAt) != identity.IdempotencyLifetime {
			t.Fatalf("idempotency lifetime metadata mismatch: scope=%q expired=%v lifetime=%v", got.ScopeID, got.Expired, got.ExpiresAt.Sub(got.CreatedAt))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("idempotency lookup transaction: %v", err)
	}
	rule := identity.RateRule{Scope: "workspace.create", Limit: 2, Window: time.Minute}
	for call := 1; call <= 3; call++ {
		decision, err := store.ConsumeRateLimit(ctx, rule, bytes.Repeat([]byte{0x61}, 32), now)
		if err != nil {
			t.Fatalf("ConsumeRateLimit() error = %v", err)
		}
		if decision.Allowed != (call <= 2) {
			t.Fatalf("call %d Allowed = %v", call, decision.Allowed)
		}
	}
}

func TestRateLimitFixedWindowResetAndRetention(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	windowStart := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	rule := identity.RateRule{Scope: "pairing.lookup", Limit: 2, Window: 5 * time.Minute}
	subjectHash := bytes.Repeat([]byte{0x62}, 32)

	for call := 1; call <= 2; call++ {
		decision, err := store.ConsumeRateLimit(ctx, rule, subjectHash, windowStart)
		if err != nil {
			t.Fatalf("initial ConsumeRateLimit() call %d error = %v", call, err)
		}
		if !decision.Allowed {
			t.Fatalf("initial call %d was denied", call)
		}
	}

	boundary := windowStart.Add(rule.Window)
	decision, err := store.ConsumeRateLimit(ctx, rule, subjectHash, boundary)
	if err != nil {
		t.Fatalf("boundary ConsumeRateLimit() error = %v", err)
	}
	if !decision.Allowed {
		t.Fatal("first request in reset window was denied")
	}

	var count int
	var storedStart time.Time
	var storedExpires time.Time
	if err := pool.QueryRow(ctx, `
select request_count, window_started_at, expires_at
from rate_limit_buckets
where scope = $1 and subject_hash = $2`, rule.Scope, subjectHash).Scan(
		&count, &storedStart, &storedExpires,
	); err != nil {
		t.Fatalf("inspect reset rate limit: %v", err)
	}
	wantExpires := boundary.Add(rule.Window + identity.RateLimitRetention)
	if count != 1 {
		t.Fatalf("reset request_count = %d", count)
	}
	if !storedStart.Equal(boundary) {
		t.Fatal("reset window_started_at differs from boundary")
	}
	if !storedExpires.Equal(wantExpires) {
		t.Fatal("reset expires_at differs from window end plus retention")
	}
}

func TestRateLimitRejectsNonPositiveWindowWithoutPersistence(t *testing.T) {
	for _, test := range []struct {
		name   string
		window time.Duration
	}{
		{name: "zero", window: 0},
		{name: "negative", window: -time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			pool := testdb.New(t)
			store := New(pool)
			_, err := store.ConsumeRateLimit(ctx, identity.RateRule{
				Scope: "workspace.create", Limit: 2, Window: test.window,
			}, bytes.Repeat([]byte{0x63}, 32), time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC))
			if !errors.Is(err, identity.ErrInvalid) {
				t.Errorf("ConsumeRateLimit() error = %v", err)
			}

			var rows int
			if err := pool.QueryRow(ctx, "select count(*) from rate_limit_buckets").Scan(&rows); err != nil {
				t.Fatalf("count rate-limit rows: %v", err)
			}
			if rows != 0 {
				t.Fatalf("rate-limit rows = %d", rows)
			}
		})
	}
}

func TestIdempotencyScopeIsolation(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	keyHash := bytes.Repeat([]byte{0x71}, 32)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		if err := tx.InsertWorkspace(ctx, workspaceTwo, now); err != nil {
			return err
		}
		for _, item := range []struct {
			scopeID     string
			requestByte byte
		}{
			{scopeID: workspaceOne, requestByte: 0x72},
			{scopeID: workspaceTwo, requestByte: 0x73},
		} {
			if err := tx.PutIdempotency(ctx, identity.IdempotencyRecord{
				ScopeID: item.scopeID, Operation: "device.rename", KeyHash: keyHash,
				WorkspaceID: item.scopeID, RequestHash: bytes.Repeat([]byte{item.requestByte}, 32),
				Response: identity.StoredResponse{Status: 200, ContentType: "application/json", Envelope: secure.Envelope{
					KeyID: "test-key", Nonce: bytes.Repeat([]byte{item.requestByte + 1}, 12), Ciphertext: []byte{item.requestByte + 2},
				}},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed scoped idempotency: %v", err)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		first, err := tx.GetIdempotency(ctx, workspaceOne, "device.rename", keyHash)
		if err != nil {
			return err
		}
		second, err := tx.GetIdempotency(ctx, workspaceTwo, "device.rename", keyHash)
		if err != nil {
			return err
		}
		if bytes.Equal(first.RequestHash, second.RequestHash) {
			t.Fatal("workspace-scoped idempotency records were not independent")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read scoped idempotency: %v", err)
	}
}

func TestPairingClaimReplayReturnsSameEncryptedGrant(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	claimHash := bytes.Repeat([]byte{0x71}, 32)
	grant := secure.Envelope{KeyID: "test-key", Nonce: bytes.Repeat([]byte{0x72}, 12), Ciphertext: []byte{0x73, 0x74}}
	pairingID := "00000000-0000-4000-8000-000000000301"
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		approver, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: deviceOne, DisplayName: "Approver", Platform: "macos", Role: "full", CreatedAt: now})
		if err != nil {
			return err
		}
		if err := tx.InsertPairing(ctx, identity.Pairing{
			ID: pairingID, ShortCode: "23456789", ClaimHash: claimHash,
			ProposedName: "Joiner", Platform: "linux", RequestedScope: "connector",
			CreatedAt: now, ExpiresAt: now.Add(identity.PairingLifetime),
			MetadataPurgeAt: now.Add(identity.PairingLifetime + identity.PairingMetadataLifetime),
		}); err != nil {
			return err
		}
		joining, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: "00000000-0000-4000-8000-000000000302", DisplayName: "Joiner", Platform: "linux", Role: "connector", CreatedAt: now})
		if err != nil {
			return err
		}
		return tx.ApprovePairing(ctx, workspaceOne, pairingID, approver.ID, joining.ID, now, now.Add(identity.ClaimLifetime), grant, now.Add(identity.ClaimLifetime+identity.PairingMetadataLifetime))
	})
	if err != nil {
		t.Fatalf("seed approved pairing: %v", err)
	}
	claim := func(claimHash []byte, claimAt time.Time) (identity.Pairing, error) {
		var pairing identity.Pairing
		err := store.WithinTx(ctx, func(tx identity.TxStore) error {
			var err error
			pairing, err = tx.LockPairingForClaim(ctx, pairingID, claimHash, claimAt)
			if err != nil {
				return err
			}
			return tx.MarkPairingClaimed(ctx, pairingID, claimAt)
		})
		return pairing, err
	}
	first, err := claim(claimHash, now)
	if err != nil {
		t.Fatalf("first claim = %v", err)
	}
	second, err := claim(claimHash, now.Add(time.Second))
	if err != nil {
		t.Fatalf("second claim = %v", err)
	}
	if !bytes.Equal(first.Grant.Ciphertext, second.Grant.Ciphertext) || !bytes.Equal(first.Grant.Nonce, second.Grant.Nonce) {
		t.Fatal("claim replay changed encrypted grant")
	}
	if _, err := claim(bytes.Repeat([]byte{0x75}, 32), now); !errors.Is(err, identity.ErrInvalidClaim) {
		t.Fatalf("wrong claim error = %v", err)
	}
}

func TestApprovedPairingDetailsExpireWhileClaimRemainsValid(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	createdAt := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	approvedAt := createdAt.Add(4 * time.Minute)
	detailsAt := createdAt.Add(identity.PairingLifetime + time.Second)
	pairingID := "00000000-0000-4000-8000-000000000331"
	joiningID := "00000000-0000-4000-8000-000000000332"
	claimHash := bytes.Repeat([]byte{0x76}, 32)
	grant := secure.Envelope{KeyID: "test-key", Nonce: bytes.Repeat([]byte{0x77}, 12), Ciphertext: []byte{0x78}}
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, createdAt); err != nil {
			return err
		}
		approver, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "Approver", Platform: "macos", Role: "full", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertPairing(ctx, identity.Pairing{
			ID: pairingID, ShortCode: "2345678D", ClaimHash: claimHash,
			ProposedName: "Joiner", Platform: "linux", RequestedScope: "connector",
			CreatedAt: createdAt, ExpiresAt: createdAt.Add(identity.PairingLifetime),
			MetadataPurgeAt: createdAt.Add(identity.PairingLifetime + identity.PairingMetadataLifetime),
		}); err != nil {
			return err
		}
		joining, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: joiningID, DisplayName: "Joiner", Platform: "linux", Role: "connector", CreatedAt: approvedAt,
		})
		if err != nil {
			return err
		}
		return tx.ApprovePairing(
			ctx, workspaceOne, pairingID, approver.ID, joining.ID,
			approvedAt, approvedAt.Add(identity.ClaimLifetime), grant,
			approvedAt.Add(identity.ClaimLifetime+identity.PairingMetadataLifetime),
		)
	})
	if err != nil {
		t.Fatalf("seed approved pairing: %v", err)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		if _, err := tx.GetPairingByID(ctx, workspaceOne, pairingID, detailsAt); !errors.Is(err, identity.ErrPairingExpired) {
			t.Fatalf("expired ID details error = %v", err)
		}
		if _, err := tx.GetPairingByShortCode(ctx, workspaceOne, "2345678D", detailsAt); !errors.Is(err, identity.ErrPairingExpired) {
			t.Fatalf("expired short-code details error = %v", err)
		}
		pairing, err := tx.LockPairingForClaim(ctx, pairingID, claimHash, detailsAt)
		if err != nil {
			return err
		}
		if !pairing.ClaimExpiresAt.Equal(approvedAt.Add(identity.ClaimLifetime)) {
			t.Fatal("private claim expiry differs from approval-relative window")
		}
		return tx.MarkPairingClaimed(ctx, pairingID, detailsAt)
	})
	if err != nil {
		t.Fatalf("expired-details/private-claim transaction: %v", err)
	}
}

func TestRenameListRevokeAndCrossWorkspaceRejection(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	hash := bytes.Repeat([]byte{0x81}, 32)
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		if err := tx.InsertWorkspace(ctx, workspaceTwo, now); err != nil {
			return err
		}
		if _, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: deviceOne, DisplayName: "MacBook Pro (3)", Platform: "macos", Role: "full", CreatedAt: now}); err != nil {
			return err
		}
		if _, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{ID: "00000000-0000-4000-8000-000000000204", DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now}); err != nil {
			return err
		}
		return tx.InsertCredential(ctx, workspaceOne, identity.CredentialRecord{DeviceID: deviceOne, Locator: "BBBBBBBBBBBBBBBBBBBBBB", Scope: "full", Hash: hash, CreatedAt: now})
	})
	if err != nil {
		t.Fatalf("seed devices: %v", err)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		renamed, err := tx.RenameDevice(ctx, workspaceOne, deviceOne, "MACBOOK PRO", now)
		if err != nil || renamed.DisplayName != "MACBOOK PRO (2)" {
			t.Fatalf("RenameDevice() = %#v, %v", renamed, err)
		}
		devices, err := tx.ListDevices(ctx, workspaceOne, deviceOne)
		if err != nil || len(devices) != 2 || !devices[0].IsCurrent {
			t.Fatalf("ListDevices() = %#v, %v", devices, err)
		}
		if _, err := tx.RenameDevice(ctx, workspaceTwo, deviceOne, "stolen", now); !errors.Is(err, identity.ErrNotFound) {
			t.Fatalf("cross-workspace rename error = %v", err)
		}
		if err := tx.RevokeDevice(ctx, workspaceOne, deviceOne, now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("device administration transaction: %v", err)
	}
	if _, err := store.Authenticate(ctx, workspaceOne, "BBBBBBBBBBBBBBBBBBBBBB", hash, now); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("revoked auth error = %v", err)
	}
}

func TestRenameUsesFourthSuffixWhenSecondAndThirdBelongToOtherDevices(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	targetID := "00000000-0000-4000-8000-000000000205"
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, now); err != nil {
			return err
		}
		for _, device := range []identity.Device{
			{ID: deviceOne, DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now},
			{ID: "00000000-0000-4000-8000-000000000202", DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now},
			{ID: "00000000-0000-4000-8000-000000000203", DisplayName: "MacBook Pro", Platform: "macos", Role: "full", CreatedAt: now},
			{ID: targetID, DisplayName: "Target", Platform: "macos", Role: "full", CreatedAt: now},
		} {
			if _, err := tx.InsertDevice(ctx, workspaceOne, device); err != nil {
				return err
			}
		}
		renamed, err := tx.RenameDevice(ctx, workspaceOne, targetID, "MACBOOK PRO", now)
		if err != nil {
			return err
		}
		if renamed.DisplayName != "MACBOOK PRO (4)" {
			t.Fatalf("RenameDevice() display name = %q", renamed.DisplayName)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("fourth suffix transaction: %v", err)
	}
}

func TestCleanupPurgesExpiredMetadata(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	_, err := store.ConsumeRateLimit(ctx, identity.RateRule{Scope: "cleanup", Limit: 1, Window: time.Minute}, bytes.Repeat([]byte{0x91}, 32), now.Add(-48*time.Hour))
	if err != nil {
		t.Fatalf("seed rate limit: %v", err)
	}
	result, err := store.Cleanup(ctx, now)
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.RateLimitRows != 1 {
		t.Fatalf("RateLimitRows = %d", result.RateLimitRows)
	}
}

func TestPurgeImagesRemovesExpiredEmptyAttachmentRevision(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	pasteID := "00000000-0000-4000-8101-000000000001"
	textRevisionID := "00000000-0000-4000-8102-000000000001"
	attachmentRevisionID := "00000000-0000-4000-8102-000000000002"

	if _, err := pool.Exec(ctx, `
insert into workspaces(id, created_at)
values ($1::uuid, $2)`, workspaceOne, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("seed attachment workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into pastes(id, workspace_id, paste_kind, created_at)
values ($1::uuid, $2::uuid, 'text', $3)`, pasteID, workspaceOne, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("seed attachment paste: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into paste_revisions(
    id, workspace_id, paste_id, server_sequence, revision_kind,
    text_key_id, text_nonce, text_ciphertext, created_at, expires_at
)
values
    ($1::uuid, $2::uuid, $3::uuid, 1, 'content',
     'test-key', decode(repeat('ab', 12), 'hex'), decode('01', 'hex'), $4, $4::timestamptz + interval '1 year'),
    ($5::uuid, $2::uuid, $3::uuid, 2, 'attachment_bundle',
     null, null, null, $6, $6::timestamptz + interval '24 hours')`,
		textRevisionID, workspaceOne, pasteID, now, attachmentRevisionID, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("seed text and empty attachment revisions: %v", err)
	}

	expired, err := store.ListExpiredImageRevisions(ctx, now, 100)
	if err != nil {
		t.Fatalf("ListExpiredImageRevisions() error = %v", err)
	}
	revisions, assets, err := store.PurgeImageRevisions(ctx, now, expired)
	if err != nil {
		t.Fatalf("PurgeImageRevisions() error = %v", err)
	}
	if revisions != 1 || assets != 0 {
		t.Fatalf("purge counts = %d/%d, want 1/0", revisions, assets)
	}

	var pasteRows, textRows, attachmentRows int
	if err := pool.QueryRow(ctx, `
select (select count(*) from pastes where workspace_id = $1::uuid and id = $2::uuid),
       (select count(*) from paste_revisions where workspace_id = $1::uuid and id = $3::uuid),
       (select count(*) from paste_revisions where workspace_id = $1::uuid and id = $4::uuid)`,
		workspaceOne, pasteID, textRevisionID, attachmentRevisionID).Scan(&pasteRows, &textRows, &attachmentRows); err != nil {
		t.Fatalf("inspect empty attachment purge: %v", err)
	}
	if pasteRows != 1 || textRows != 1 || attachmentRows != 0 {
		t.Fatalf("remaining rows = paste:%d text:%d attachment:%d, want 1/1/0", pasteRows, textRows, attachmentRows)
	}
}

func TestPurgeImageRevisionsCountsAllSelectedAssetsInIndexOrder(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	pasteID := "00000000-0000-4000-8111-000000000001"
	textRevisionID := "00000000-0000-4000-8112-000000000001"
	attachmentRevisionID := "00000000-0000-4000-8112-000000000002"

	if _, err := pool.Exec(ctx, `
insert into workspaces(id, created_at)
values ($1::uuid, $2)`, workspaceOne, now.Add(-72*time.Hour)); err != nil {
		t.Fatalf("seed multi-asset workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into pastes(id, workspace_id, paste_kind, created_at)
values ($1::uuid, $2::uuid, 'text', $3)`, pasteID, workspaceOne, now.Add(-72*time.Hour)); err != nil {
		t.Fatalf("seed multi-asset paste: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into paste_revisions(
    id, workspace_id, paste_id, server_sequence, revision_kind,
    text_key_id, text_nonce, text_ciphertext, created_at, expires_at
)
values
    ($1::uuid, $2::uuid, $3::uuid, 1, 'content',
     'test-key', decode(repeat('ab', 12), 'hex'), decode('01', 'hex'), $4, $4::timestamptz + interval '1 year'),
    ($5::uuid, $2::uuid, $3::uuid, 2, 'attachment_bundle',
     null, null, null, $6, $6::timestamptz + interval '24 hours')`,
		textRevisionID, workspaceOne, pasteID, now.Add(-72*time.Hour), attachmentRevisionID, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("seed multi-asset revisions: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into paste_assets(
    workspace_id, paste_id, revision_id, asset_index, mime_type, width, height,
    byte_size, storage_key, image_key_id, image_nonce, created_at, expires_at
)
values
    ($1::uuid, $2::uuid, $3::uuid, 2, 'image/png', 3, 3, 3,
     'cleanup/multi/2', 'test-key', decode(repeat('a2', 12), 'hex'), $4, $4::timestamptz + interval '24 hours'),
    ($1::uuid, $2::uuid, $3::uuid, 0, 'image/png', 1, 1, 1,
     'cleanup/multi/0', 'test-key', decode(repeat('a0', 12), 'hex'), $5, $5::timestamptz + interval '24 hours'),
    ($1::uuid, $2::uuid, $3::uuid, 1, 'image/png', 2, 2, 2,
     'cleanup/multi/1', 'test-key', decode(repeat('a1', 12), 'hex'), $6, $6::timestamptz + interval '24 hours')`,
		workspaceOne, pasteID, attachmentRevisionID,
		now.Add(-36*time.Hour), now.Add(-48*time.Hour), now.Add(-12*time.Hour)); err != nil {
		t.Fatalf("seed out-of-order multi-asset rows: %v", err)
	}

	expired, err := store.ListExpiredImageRevisions(ctx, now, 100)
	if err != nil {
		t.Fatalf("ListExpiredImageRevisions() error = %v", err)
	}
	if len(expired) != 1 || expired[0].WorkspaceID != workspaceOne || expired[0].PasteID != pasteID || expired[0].RevisionID != attachmentRevisionID {
		t.Fatalf("expired revisions = %#v", expired)
	}
	if len(expired[0].Assets) != 3 || expired[0].Assets[0].AssetIndex != 0 || expired[0].Assets[1].AssetIndex != 1 || expired[0].Assets[2].AssetIndex != 2 {
		t.Fatalf("ordered assets = %#v", expired[0].Assets)
	}

	revisions, assets, err := store.PurgeImageRevisions(ctx, now, expired)
	if err != nil {
		t.Fatalf("PurgeImageRevisions() error = %v", err)
	}
	if revisions != 1 || assets != 3 {
		t.Fatalf("purge counts = %d/%d, want 1/3", revisions, assets)
	}

	var pasteRows, textRows, attachmentRows, assetRows int
	if err := pool.QueryRow(ctx, `
select (select count(*) from pastes where workspace_id = $1::uuid and id = $2::uuid),
       (select count(*) from paste_revisions where workspace_id = $1::uuid and id = $3::uuid),
       (select count(*) from paste_revisions where workspace_id = $1::uuid and id = $4::uuid),
       (select count(*) from paste_assets where workspace_id = $1::uuid and revision_id = $4::uuid)`,
		workspaceOne, pasteID, textRevisionID, attachmentRevisionID).Scan(&pasteRows, &textRows, &attachmentRows, &assetRows); err != nil {
		t.Fatalf("inspect multi-asset purge: %v", err)
	}
	if pasteRows != 1 || textRows != 1 || attachmentRows != 0 || assetRows != 0 {
		t.Fatalf("remaining rows = paste:%d text:%d attachment:%d assets:%d, want 1/1/0/0", pasteRows, textRows, attachmentRows, assetRows)
	}
}

func TestPurgeImageRevisionsRemovesSelectedLegacyOrphanPasteInSameCycle(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	pasteID := "00000000-0000-4000-8121-000000000001"
	revisionID := "00000000-0000-4000-8122-000000000001"

	if _, err := pool.Exec(ctx, `
insert into workspaces(id, created_at)
values ($1::uuid, $2)`, workspaceOne, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("seed legacy orphan workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into pastes(id, workspace_id, paste_kind, created_at)
values ($1::uuid, $2::uuid, 'image_bundle', $3)`, pasteID, workspaceOne, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("seed legacy orphan paste: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into paste_revisions(
    id, workspace_id, paste_id, server_sequence, revision_kind, created_at, expires_at
)
values ($1::uuid, $2::uuid, $3::uuid, 1, 'image_bundle', $4, $4::timestamptz + interval '24 hours')`,
		revisionID, workspaceOne, pasteID, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("seed legacy orphan revision: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into paste_assets(
    workspace_id, paste_id, revision_id, asset_index, mime_type, width, height,
    byte_size, storage_key, image_key_id, image_nonce, created_at, expires_at
)
values ($1::uuid, $2::uuid, $3::uuid, 0, 'image/png', 1, 1, 1,
        'cleanup/orphan/0', 'test-key', decode(repeat('ab', 12), 'hex'), $4, $4::timestamptz + interval '24 hours')`,
		workspaceOne, pasteID, revisionID, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("seed legacy orphan asset: %v", err)
	}

	expired, err := store.ListExpiredImageRevisions(ctx, now, 100)
	if err != nil {
		t.Fatalf("ListExpiredImageRevisions() error = %v", err)
	}
	revisions, assets, err := store.PurgeImageRevisions(ctx, now, expired)
	if err != nil {
		t.Fatalf("PurgeImageRevisions() error = %v", err)
	}
	if revisions != 1 || assets != 1 {
		t.Fatalf("purge counts = %d/%d, want 1/1", revisions, assets)
	}

	var pasteRows, revisionRows, assetRows int
	if err := pool.QueryRow(ctx, `
select (select count(*) from pastes where workspace_id = $1::uuid and id = $2::uuid),
       (select count(*) from paste_revisions where workspace_id = $1::uuid and id = $3::uuid),
       (select count(*) from paste_assets where workspace_id = $1::uuid and revision_id = $3::uuid)`,
		workspaceOne, pasteID, revisionID).Scan(&pasteRows, &revisionRows, &assetRows); err != nil {
		t.Fatalf("inspect legacy orphan purge: %v", err)
	}
	if pasteRows != 0 || revisionRows != 0 || assetRows != 0 {
		t.Fatalf("remaining rows = paste:%d revision:%d assets:%d, want 0/0/0", pasteRows, revisionRows, assetRows)
	}
}

func TestPurgeImagesBoundsFilesystemSelectionAndDatabaseDeletion(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
insert into workspaces(id, created_at)
values ($1::uuid, $2)`, workspaceOne, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("seed image workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into pastes(id, workspace_id, paste_kind, created_at)
select ('00000000-0000-4000-8103-' || lpad(n::text, 12, '0'))::uuid,
       $1::uuid, 'image_bundle', $2::timestamptz - interval '48 hours'
from generate_series(1, 101) as rows(n)`, workspaceOne, now); err != nil {
		t.Fatalf("seed image pastes: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into paste_revisions(
    id, workspace_id, paste_id, server_sequence, revision_kind, created_at, expires_at
)
select ('00000000-0000-4000-8104-' || lpad(n::text, 12, '0'))::uuid,
       $1::uuid,
       ('00000000-0000-4000-8103-' || lpad(n::text, 12, '0'))::uuid,
       n, 'image_bundle', $2::timestamptz - interval '48 hours', $2::timestamptz - interval '24 hours'
from generate_series(1, 101) as rows(n)`, workspaceOne, now); err != nil {
		t.Fatalf("seed image revisions: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into paste_assets(
    workspace_id, paste_id, revision_id, asset_index, mime_type, width, height,
    byte_size, storage_key, image_key_id, image_nonce, created_at, expires_at
)
select $1::uuid,
       ('00000000-0000-4000-8103-' || lpad(n::text, 12, '0'))::uuid,
       ('00000000-0000-4000-8104-' || lpad(n::text, 12, '0'))::uuid,
       0, 'image/png', 1, 1, 1,
       'storage/' || n, 'test-key', decode(repeat('ab', 12), 'hex'),
       $2::timestamptz - interval '48 hours', $2::timestamptz - interval '24 hours'
from generate_series(1, 101) as rows(n)`, workspaceOne, now); err != nil {
		t.Fatalf("seed image assets: %v", err)
	}

	expired, err := store.ListExpiredImageRevisions(ctx, now, 100)
	if err != nil {
		t.Fatalf("ListExpiredImageRevisions() error = %v", err)
	}
	if len(expired) != 100 {
		t.Fatalf("ListExpiredImageRevisions() count = %d, want 100", len(expired))
	}
	revisions, assets, err := store.PurgeImageRevisions(ctx, now, expired)
	if err != nil {
		t.Fatalf("PurgeImageRevisions() error = %v", err)
	}
	if revisions != 100 || assets != 100 {
		t.Fatalf("PurgeImageRevisions() counts = %d/%d, want 100/100", revisions, assets)
	}
	var remainingRevisions, remainingAssets int
	if err := pool.QueryRow(ctx, `
select (select count(*) from paste_revisions where revision_kind = 'image_bundle'),
       (select count(*) from paste_assets)`).Scan(&remainingRevisions, &remainingAssets); err != nil {
		t.Fatalf("inspect remaining images: %v", err)
	}
	if remainingRevisions != 1 || remainingAssets != 1 {
		t.Fatalf("remaining image rows = %d/%d, want 1/1", remainingRevisions, remainingAssets)
	}
}

func TestCleanupBoundsEachMetadataPurge(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)

	if _, err := pool.Exec(ctx, `
insert into workspaces(id, created_at)
values ($1::uuid, $2)`, workspaceOne, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("seed cleanup workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into pairing_requests(
    id, short_code, claim_hash, proposed_name, platform, requested_scope,
    created_at, expires_at, metadata_purge_at
)
select ('00000000-0000-4000-8101-' || lpad(n::text, 12, '0'))::uuid,
       'AAAAAA'
           || substr('23456789ABCDEFGHJKMNPQRSTUVWXYZ', ((n - 1) / 31) + 1, 1)
           || substr('23456789ABCDEFGHJKMNPQRSTUVWXYZ', ((n - 1) % 31) + 1, 1),
       decode(repeat('a1', 32), 'hex'), 'Pending ' || n, 'linux', 'connector',
       $1::timestamptz - interval '2 hours',
       $1::timestamptz - interval '1 hour',
       $1::timestamptz - interval '1 hour'
from generate_series(1, 101) as rows(n)`, now); err != nil {
		t.Fatalf("seed expired pairings: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into idempotency_records(
    scope_id, operation, key_hash, workspace_id, request_hash,
    response_status, response_content_type, response_key_id, response_nonce,
    response_ciphertext, created_at, expires_at
)
select 'public', 'cleanup.test', decode(lpad(to_hex(n), 64, '0'), 'hex'), null,
       decode(repeat('b1', 32), 'hex'), 200, 'application/json', 'test-key',
       decode(repeat('b2', 12), 'hex'), decode('b3', 'hex'),
       $1::timestamptz - interval '25 hours', $1::timestamptz - interval '1 hour'
from generate_series(1, 101) as rows(n)`, now); err != nil {
		t.Fatalf("seed expired idempotency records: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into workspace_events(
    workspace_id, sequence, event_type, object_id, created_at, expires_at
)
select $1::uuid, n, 'device.added',
       ('00000000-0000-4000-8102-' || lpad(n::text, 12, '0'))::uuid,
       $2::timestamptz - interval '2 hours', $2::timestamptz - interval '1 hour'
from generate_series(1, 101) as rows(n)`, workspaceOne, now); err != nil {
		t.Fatalf("seed expired workspace events: %v", err)
	}
	if _, err := pool.Exec(ctx, `
insert into rate_limit_buckets(
    scope, subject_hash, window_started_at, request_count, expires_at
)
select 'cleanup.test', decode(lpad(to_hex(n), 64, '0'), 'hex'),
       $1::timestamptz - interval '2 hours', 1, $1::timestamptz - interval '1 hour'
from generate_series(1, 101) as rows(n)`, now); err != nil {
		t.Fatalf("seed expired rate limit buckets: %v", err)
	}

	want := []int64{100, 1, 0}
	for call, expected := range want {
		result, err := store.Cleanup(ctx, now)
		if err != nil {
			t.Fatalf("Cleanup() call %d error = %v", call+1, err)
		}
		if result.PairingRows != expected || result.IdempotencyRows != expected ||
			result.EventRows != expected || result.RateLimitRows != expected {
			t.Fatalf(
				"Cleanup() call %d rows = pairing:%d idempotency:%d events:%d rate_limits:%d, want %d each",
				call+1, result.PairingRows, result.IdempotencyRows, result.EventRows, result.RateLimitRows, expected,
			)
		}
	}
	var retentionFloor int64
	if err := pool.QueryRow(ctx, `select event_retention_floor from workspaces where id = $1::uuid`, workspaceOne).Scan(&retentionFloor); err != nil {
		t.Fatalf("inspect event retention floor: %v", err)
	}
	if retentionFloor != 101 {
		t.Fatalf("event retention floor = %d, want 101", retentionFloor)
	}
}

func TestClaimAndCleanupSerializeGrantValidity(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	createdAt := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	claimAt := createdAt.Add(4*time.Minute + 59*time.Second)
	cleanupAt := createdAt.Add(6 * time.Minute)
	pairingID := "00000000-0000-4000-8000-000000000311"
	joiningDeviceID := "00000000-0000-4000-8000-000000000312"
	claimHash := bytes.Repeat([]byte{0xa1}, 32)
	credentialHash := bytes.Repeat([]byte{0xa2}, 32)
	credentialLocator := "CCCCCCCCCCCCCCCCCCCCCC"
	grant := secure.Envelope{KeyID: "test-key", Nonce: bytes.Repeat([]byte{0xa3}, 12), Ciphertext: []byte{0xa4}}
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, createdAt); err != nil {
			return err
		}
		approver, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "Approver", Platform: "macos", Role: "full", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertPairing(ctx, identity.Pairing{
			ID: pairingID, ShortCode: "2345678A", ClaimHash: claimHash,
			ProposedName: "Joiner", Platform: "linux", RequestedScope: "connector",
			CreatedAt: createdAt, ExpiresAt: createdAt.Add(identity.PairingLifetime),
			MetadataPurgeAt: createdAt.Add(identity.PairingLifetime + identity.PairingMetadataLifetime),
		}); err != nil {
			return err
		}
		joining, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: joiningDeviceID, DisplayName: "Joiner", Platform: "linux", Role: "connector", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertCredential(ctx, workspaceOne, identity.CredentialRecord{
			DeviceID: joining.ID, Locator: credentialLocator, Scope: "connector", Hash: credentialHash, CreatedAt: createdAt,
		}); err != nil {
			return err
		}
		return tx.ApprovePairing(
			ctx, workspaceOne, pairingID, approver.ID, joining.ID,
			createdAt, createdAt.Add(identity.ClaimLifetime), grant,
			createdAt.Add(identity.ClaimLifetime+identity.PairingMetadataLifetime),
		)
	})
	if err != nil {
		t.Fatalf("seed claim/cleanup race: %v", err)
	}

	type cleanupOutcome struct {
		result identity.CleanupResult
		err    error
	}
	start := make(chan struct{})
	claimDone := make(chan error, 1)
	cleanupDone := make(chan cleanupOutcome, 1)
	go func() {
		<-start
		err := store.WithinTx(ctx, func(tx identity.TxStore) error {
			if _, err := tx.LockPairingForClaim(ctx, pairingID, claimHash, claimAt); err != nil {
				return err
			}
			return tx.MarkPairingClaimed(ctx, pairingID, claimAt)
		})
		claimDone <- err
	}()
	go func() {
		<-start
		result, err := store.Cleanup(ctx, cleanupAt)
		cleanupDone <- cleanupOutcome{result: result, err: err}
	}()
	close(start)
	claimErr := <-claimDone
	cleanup := <-cleanupDone
	if cleanup.err != nil {
		t.Fatalf("Cleanup() error = %v", cleanup.err)
	}

	var claimedAt, invalidatedAt *time.Time
	if err := pool.QueryRow(ctx, `
select claimed_at, claim_invalidated_at
from pairing_requests
where workspace_id = $1::uuid and id = $2::uuid`, workspaceOne, pairingID).Scan(&claimedAt, &invalidatedAt); err != nil {
		t.Fatalf("inspect pairing terminal state: %v", err)
	}
	switch {
	case claimErr == nil:
		if cleanup.result.RevokedDevices != 0 || claimedAt == nil || invalidatedAt != nil {
			t.Fatalf("claim-won state: revoked=%d claimed=%v invalidated=%v", cleanup.result.RevokedDevices, claimedAt != nil, invalidatedAt != nil)
		}
		if _, err := store.Authenticate(ctx, workspaceOne, credentialLocator, credentialHash, cleanupAt); err != nil {
			t.Fatalf("claim-won credential authentication: %v", err)
		}
	case errors.Is(claimErr, identity.ErrPairingDenied):
		if cleanup.result.RevokedDevices != 1 || claimedAt != nil || invalidatedAt == nil {
			t.Fatalf("cleanup-won state: revoked=%d claimed=%v invalidated=%v", cleanup.result.RevokedDevices, claimedAt != nil, invalidatedAt != nil)
		}
		if _, err := store.Authenticate(ctx, workspaceOne, credentialLocator, credentialHash, cleanupAt); !errors.Is(err, identity.ErrUnauthorized) {
			t.Fatalf("cleanup-won authentication error = %v", err)
		}
		var eventCount int
		if err := pool.QueryRow(ctx, `
select count(*)
from workspace_events
where workspace_id = $1::uuid and event_type = 'device.revoked' and object_id = $2::uuid`,
			workspaceOne, joiningDeviceID).Scan(&eventCount); err != nil {
			t.Fatalf("count cleanup event: %v", err)
		}
		if eventCount != 1 {
			t.Fatalf("cleanup device.revoked events = %d", eventCount)
		}
	default:
		t.Fatalf("claim error = %v", claimErr)
	}
}

func TestCleanupWinsDeterministicallyAndRevokesGrant(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	createdAt := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	cleanupAt := createdAt.Add(6 * time.Minute)
	pairingID := "00000000-0000-4000-8000-000000000321"
	joiningDeviceID := "00000000-0000-4000-8000-000000000322"
	claimHash := bytes.Repeat([]byte{0xb1}, 32)
	credentialHash := bytes.Repeat([]byte{0xb2}, 32)
	credentialLocator := "DDDDDDDDDDDDDDDDDDDDDD"
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, createdAt); err != nil {
			return err
		}
		approver, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "Approver", Platform: "macos", Role: "full", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertPairing(ctx, identity.Pairing{
			ID: pairingID, ShortCode: "2345678B", ClaimHash: claimHash,
			ProposedName: "Joiner", Platform: "linux", RequestedScope: "connector",
			CreatedAt: createdAt, ExpiresAt: createdAt.Add(identity.PairingLifetime),
			MetadataPurgeAt: createdAt.Add(identity.PairingLifetime + identity.PairingMetadataLifetime),
		}); err != nil {
			return err
		}
		joining, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: joiningDeviceID, DisplayName: "Joiner", Platform: "linux", Role: "connector", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertCredential(ctx, workspaceOne, identity.CredentialRecord{
			DeviceID: joining.ID, Locator: credentialLocator, Scope: "connector", Hash: credentialHash, CreatedAt: createdAt,
		}); err != nil {
			return err
		}
		grant := secure.Envelope{KeyID: "test-key", Nonce: bytes.Repeat([]byte{0xb3}, 12), Ciphertext: []byte{0xb4}}
		return tx.ApprovePairing(
			ctx, workspaceOne, pairingID, approver.ID, joining.ID,
			createdAt, createdAt.Add(identity.ClaimLifetime), grant,
			createdAt.Add(identity.ClaimLifetime+identity.PairingMetadataLifetime),
		)
	})
	if err != nil {
		t.Fatalf("seed deterministic cleanup: %v", err)
	}
	result, err := store.Cleanup(ctx, cleanupAt)
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.RevokedDevices != 1 {
		t.Fatalf("RevokedDevices = %d", result.RevokedDevices)
	}
	if _, err := store.Authenticate(ctx, workspaceOne, credentialLocator, credentialHash, cleanupAt); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("revoked credential authentication error = %v", err)
	}
	var deviceRevoked, credentialRevoked, invalidatedAt *time.Time
	var eventCount int
	if err := pool.QueryRow(ctx, `
select d.revoked_at, c.revoked_at, p.claim_invalidated_at,
       (select count(*) from workspace_events e
        where e.workspace_id = p.workspace_id and e.event_type = 'device.revoked' and e.object_id = p.device_id)
from pairing_requests p
join devices d on d.workspace_id = p.workspace_id and d.id = p.device_id
join credentials c on c.workspace_id = d.workspace_id and c.device_id = d.id
where p.workspace_id = $1::uuid and p.id = $2::uuid`, workspaceOne, pairingID).Scan(
		&deviceRevoked, &credentialRevoked, &invalidatedAt, &eventCount,
	); err != nil {
		t.Fatalf("inspect cleanup state: %v", err)
	}
	if deviceRevoked == nil || credentialRevoked == nil || invalidatedAt == nil || eventCount != 1 {
		t.Fatalf("cleanup state metadata: device=%v credential=%v invalidated=%v events=%d", deviceRevoked != nil, credentialRevoked != nil, invalidatedAt != nil, eventCount)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		_, err := tx.LockPairingForClaim(ctx, pairingID, claimHash, cleanupAt)
		return err
	})
	if !errors.Is(err, identity.ErrPairingDenied) {
		t.Fatalf("claim after cleanup error = %v", err)
	}
}

func TestCleanupDoesNotDuplicateRevokedDeviceEvent(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	createdAt := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	cleanupAt := createdAt.Add(6 * time.Minute)
	pairingID := "00000000-0000-4000-8000-000000000331"
	joiningDeviceID := "00000000-0000-4000-8000-000000000332"
	claimHash := bytes.Repeat([]byte{0xc1}, 32)
	credentialHash := bytes.Repeat([]byte{0xc2}, 32)
	credentialLocator := "EEEEEEEEEEEEEEEEEEEEEE"

	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceOne, createdAt); err != nil {
			return err
		}
		approver, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: deviceOne, DisplayName: "Approver", Platform: "macos", Role: "full", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertPairing(ctx, identity.Pairing{
			ID: pairingID, ShortCode: "2345678C", ClaimHash: claimHash,
			ProposedName: "Joiner", Platform: "linux", RequestedScope: "connector",
			CreatedAt: createdAt, ExpiresAt: createdAt.Add(identity.PairingLifetime),
			MetadataPurgeAt: createdAt.Add(identity.PairingLifetime + identity.PairingMetadataLifetime),
		}); err != nil {
			return err
		}
		joining, err := tx.InsertDevice(ctx, workspaceOne, identity.Device{
			ID: joiningDeviceID, DisplayName: "Joiner", Platform: "linux", Role: "connector", CreatedAt: createdAt,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertCredential(ctx, workspaceOne, identity.CredentialRecord{
			DeviceID: joining.ID, Locator: credentialLocator, Scope: "connector", Hash: credentialHash, CreatedAt: createdAt,
		}); err != nil {
			return err
		}
		grant := secure.Envelope{KeyID: "test-key", Nonce: bytes.Repeat([]byte{0xc3}, 12), Ciphertext: []byte{0xc4}}
		if err := tx.ApprovePairing(
			ctx, workspaceOne, pairingID, approver.ID, joining.ID,
			createdAt, createdAt.Add(identity.ClaimLifetime), grant,
			createdAt.Add(identity.ClaimLifetime+identity.PairingMetadataLifetime),
		); err != nil {
			return err
		}
		if err := tx.RevokeDevice(ctx, workspaceOne, joining.ID, createdAt.Add(time.Minute)); err != nil {
			return err
		}
		return tx.InsertEvent(ctx, workspaceOne, "device.revoked", joining.ID, createdAt.Add(time.Minute))
	})
	if err != nil {
		t.Fatalf("seed already-revoked grant: %v", err)
	}

	result, err := store.Cleanup(ctx, cleanupAt)
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.RevokedDevices != 0 {
		t.Fatalf("RevokedDevices = %d", result.RevokedDevices)
	}

	var deviceRevoked, credentialRevoked, invalidatedAt *time.Time
	var eventCount int
	if err := pool.QueryRow(ctx, `
select d.revoked_at, c.revoked_at, p.claim_invalidated_at,
       (select count(*) from workspace_events e
        where e.workspace_id = p.workspace_id and e.event_type = 'device.revoked' and e.object_id = p.device_id)
from pairing_requests p
join devices d on d.workspace_id = p.workspace_id and d.id = p.device_id
join credentials c on c.workspace_id = d.workspace_id and c.device_id = d.id
where p.workspace_id = $1::uuid and p.id = $2::uuid`, workspaceOne, pairingID).Scan(
		&deviceRevoked, &credentialRevoked, &invalidatedAt, &eventCount,
	); err != nil {
		t.Fatalf("inspect already-revoked cleanup state: %v", err)
	}
	if deviceRevoked == nil || credentialRevoked == nil || invalidatedAt == nil {
		t.Fatalf(
			"already-revoked cleanup state: device=%v credential=%v invalidated=%v",
			deviceRevoked != nil, credentialRevoked != nil, invalidatedAt != nil,
		)
	}
	if eventCount != 1 {
		t.Fatalf("device.revoked event count = %d", eventCount)
	}
	err = store.WithinTx(ctx, func(tx identity.TxStore) error {
		_, err := tx.LockPairingForClaim(ctx, pairingID, claimHash, cleanupAt)
		return err
	})
	if !errors.Is(err, identity.ErrPairingDenied) {
		t.Fatalf("claim after cleanup error = %v", err)
	}
}

func TestCleanupPrioritizesOldestExpiredGrantAcrossWorkspaces(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	store := New(pool)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)

	seedExpiredCleanupGrants(t, ctx, pool, now, workspaceOne, deviceOne, "8201", "8301", 0, 101, 0, 1)
	seedExpiredCleanupGrants(
		t, ctx, pool, now, workspaceTwo, "00000000-0000-4000-8000-000000000202",
		"8202", "8302", 200, 1, 199, 1,
	)

	result, err := store.Cleanup(ctx, now)
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.RevokedDevices != 100 {
		t.Fatalf("RevokedDevices = %d", result.RevokedDevices)
	}
	var highWorkspaceInvalidated, highWorkspaceRevoked int
	if err := pool.QueryRow(ctx, `
select (select count(*) from pairing_requests
        where workspace_id = $1::uuid and claim_invalidated_at is not null),
       (select count(*) from devices
        where workspace_id = $1::uuid and role = 'connector' and revoked_at is not null)`, workspaceTwo).Scan(
		&highWorkspaceInvalidated, &highWorkspaceRevoked,
	); err != nil {
		t.Fatalf("inspect oldest high-workspace grant: %v", err)
	}
	if highWorkspaceInvalidated != 1 || highWorkspaceRevoked != 1 {
		t.Fatalf(
			"oldest high-workspace grant state = invalidated:%d revoked:%d",
			highWorkspaceInvalidated, highWorkspaceRevoked,
		)
	}
}

func TestConcurrentCleanupAcrossWorkspacesReachesConsistentTerminalState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	basePool := testdb.New(t)
	pool, applicationName := newCleanupTestPool(t, ctx, basePool)
	store := New(pool)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)

	seedExpiredCleanupGrants(t, ctx, pool, now, workspaceOne, deviceOne, "8401", "8501", 0, 60, 0, 2)
	seedExpiredCleanupGrants(
		t, ctx, pool, now, workspaceTwo, "00000000-0000-4000-8000-000000000202",
		"8402", "8502", 100, 60, -1, 2,
	)

	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin cleanup advisory lock blocker: %v", err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err := blocker.Exec(ctx, `
select pg_advisory_xact_lock(hashtextextended($1, 0))`, cleanupAdvisoryLockName); err != nil {
		t.Fatalf("acquire cleanup advisory lock blocker: %v", err)
	}
	var blockerBackendPID int
	if err := blocker.QueryRow(ctx, "select pg_backend_pid()").Scan(&blockerBackendPID); err != nil {
		t.Fatalf("read cleanup blocker backend PID: %v", err)
	}

	type cleanupOutcome struct {
		result identity.CleanupResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan cleanupOutcome, 2)
	for worker := 0; worker < 2; worker++ {
		go func() {
			<-start
			result, err := store.Cleanup(ctx, now)
			outcomes <- cleanupOutcome{result: result, err: err}
		}()
	}
	close(start)
	waitCtx, cancelWait := context.WithTimeout(ctx, 2*time.Second)
	defer cancelWait()
	waitForCleanupWorkersBlocked(t, waitCtx, pool, applicationName, blockerBackendPID)

	var invalidatedWhileBlocked int
	if err := pool.QueryRow(ctx, "select count(*) from pairing_requests where claim_invalidated_at is not null").Scan(
		&invalidatedWhileBlocked,
	); err != nil {
		t.Fatalf("inspect blocked cleanup state: %v", err)
	}
	if invalidatedWhileBlocked != 0 {
		t.Fatalf("invalidated grants while cleanup lock blocked = %d", invalidatedWhileBlocked)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("release cleanup advisory lock blocker: %v", err)
	}

	totalRevoked := int64(0)
	for worker := 0; worker < 2; worker++ {
		outcome := <-outcomes
		if outcome.err != nil {
			t.Fatalf("concurrent Cleanup() error = %v", outcome.err)
		}
		totalRevoked += outcome.result.RevokedDevices
	}
	if totalRevoked != 120 {
		t.Fatalf("concurrent RevokedDevices total = %d", totalRevoked)
	}

	var invalidated, revokedDevices, revokedCredentials, revokedEvents int
	if err := pool.QueryRow(ctx, `
select (select count(*) from pairing_requests where claim_invalidated_at is not null),
       (select count(*) from devices where role = 'connector' and revoked_at is not null),
       (select count(*) from credentials where revoked_at is not null),
       (select count(*) from workspace_events where event_type = 'device.revoked')`).Scan(
		&invalidated, &revokedDevices, &revokedCredentials, &revokedEvents,
	); err != nil {
		t.Fatalf("inspect concurrent cleanup terminal state: %v", err)
	}
	if invalidated != 120 || revokedDevices != 120 || revokedCredentials != 120 || revokedEvents != 120 {
		t.Fatalf(
			"terminal counts = invalidated:%d devices:%d credentials:%d events:%d",
			invalidated, revokedDevices, revokedCredentials, revokedEvents,
		)
	}
	result, err := store.Cleanup(ctx, now)
	if err != nil {
		t.Fatalf("terminal Cleanup() error = %v", err)
	}
	if result.RevokedDevices != 0 {
		t.Fatalf("terminal RevokedDevices = %d", result.RevokedDevices)
	}
}

func newCleanupTestPool(
	t *testing.T,
	ctx context.Context,
	basePool *pgxpool.Pool,
) (*pgxpool.Pool, string) {
	t.Helper()
	applicationName := fmt.Sprintf(
		"mcpaste-cleanup-test-%d-%d",
		os.Getpid(), cleanupTestApplicationCounter.Add(1),
	)
	config := basePool.Config()
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	config.ConnConfig.RuntimeParams["application_name"] = applicationName
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open dedicated cleanup test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("connect dedicated cleanup test pool: %v", err)
	}
	return pool, applicationName
}

func waitForCleanupWorkersBlocked(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	applicationName string,
	blockerBackendPID int,
) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waitingWorkers int
		if err := pool.QueryRow(ctx, `
select count(*)
from pg_stat_activity waiting_cleanup
where waiting_cleanup.datname = current_database()
  and waiting_cleanup.application_name = $2
  and waiting_cleanup.wait_event_type = 'Lock'
  and position('pg_advisory_xact_lock(hashtextextended' in waiting_cleanup.query) > 0
  and $1::integer = any(pg_blocking_pids(waiting_cleanup.pid))`, blockerBackendPID, applicationName).Scan(&waitingWorkers); err != nil {
			if ctx.Err() != nil {
				t.Fatalf("cleanup workers waiting on blocker = %d, want 2", waitingWorkers)
			}
			t.Fatalf("inspect blocked cleanup workers: %v", err)
		}
		if waitingWorkers == 2 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("cleanup workers waiting on blocker = %d, want 2", waitingWorkers)
		case <-ticker.C:
		}
	}
}

func seedExpiredCleanupGrants(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	now time.Time,
	workspaceID string,
	approverDeviceID string,
	deviceUUIDGroup string,
	pairingUUIDGroup string,
	ordinalBase int,
	count int,
	expiryOffset int,
	expiryStride int,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
insert into workspaces(id, created_at)
values ($1::uuid, $2)`, workspaceID, now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("seed cleanup workspace %s: %v", workspaceID, err)
	}
	if _, err := pool.Exec(ctx, `
insert into devices(id, workspace_id, display_name, platform, role, created_at)
values ($1::uuid, $2::uuid, 'Approver', 'macos', 'full', $3)`,
		approverDeviceID, workspaceID, now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("seed cleanup approver for workspace %s: %v", workspaceID, err)
	}
	if _, err := pool.Exec(ctx, `
insert into devices(id, workspace_id, display_name, platform, role, created_at)
select ('00000000-0000-4000-' || $3 || '-' || lpad(($4 + n)::text, 12, '0'))::uuid,
       $1::uuid, 'Joiner ' || n, 'linux', 'connector', $2
from generate_series(1, $5) as rows(n)`,
		workspaceID, now.Add(-10*time.Minute), deviceUUIDGroup, ordinalBase, count); err != nil {
		t.Fatalf("seed cleanup joining devices for workspace %s: %v", workspaceID, err)
	}
	if _, err := pool.Exec(ctx, `
insert into credentials(workspace_id, device_id, token_id, scope, secret_hash, created_at)
select $1::uuid,
       ('00000000-0000-4000-' || $3 || '-' || lpad(($4 + n)::text, 12, '0'))::uuid,
       lpad(($4 + n)::text, 22, 'A'), 'connector', decode(repeat('d1', 32), 'hex'), $2
from generate_series(1, $5) as rows(n)`,
		workspaceID, now.Add(-10*time.Minute), deviceUUIDGroup, ordinalBase, count); err != nil {
		t.Fatalf("seed cleanup credentials for workspace %s: %v", workspaceID, err)
	}
	if _, err := pool.Exec(ctx, `
insert into pairing_requests(
    id, short_code, claim_hash, proposed_name, platform, requested_scope,
    workspace_id, approved_by_device_id, device_id,
    created_at, expires_at, approved_at, claim_expires_at,
    grant_key_id, grant_nonce, grant_ciphertext, metadata_purge_at
)
select ('00000000-0000-4000-' || $4 || '-' || lpad(($5 + n)::text, 12, '0'))::uuid,
       'AAAAA'
           || substr('23456789ABCDEFGHJKMNPQRSTUVWXYZ', ((($5 + n - 1) / 961) % 31) + 1, 1)
           || substr('23456789ABCDEFGHJKMNPQRSTUVWXYZ', ((($5 + n - 1) / 31) % 31) + 1, 1)
           || substr('23456789ABCDEFGHJKMNPQRSTUVWXYZ', (($5 + n - 1) % 31) + 1, 1),
       decode(repeat('d2', 32), 'hex'), 'Joiner ' || n, 'linux', 'connector',
       $1::uuid, $2::uuid,
       ('00000000-0000-4000-' || $3 || '-' || lpad(($5 + n)::text, 12, '0'))::uuid,
       $6::timestamptz - interval '10 minutes', $6::timestamptz + interval '1 hour',
       $6::timestamptz - interval '9 minutes',
       $6::timestamptz - (($7 + $8 * n) * interval '1 second'),
       'test-key', decode(repeat('d3', 12), 'hex'), decode('d4', 'hex'),
       $6::timestamptz + interval '24 hours'
from generate_series(1, $9) as rows(n)`,
		workspaceID, approverDeviceID, deviceUUIDGroup, pairingUUIDGroup, ordinalBase,
		now, expiryOffset, expiryStride, count,
	); err != nil {
		t.Fatalf("seed expired cleanup grants for workspace %s: %v", workspaceID, err)
	}
}
