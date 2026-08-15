package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/1yoouoo/mcpaste/internal/testdb"
)

func TestPasteAggregateCombinesAndPreservesIndependentComponents(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	createdAt := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000901"
	pasteID := "00000000-0000-4000-8000-000000000902"
	textOneID := "00000000-0000-4000-8000-000000000903"
	attachmentOneID := "00000000-0000-4000-8000-000000000904"
	textTwoID := "00000000-0000-4000-8000-000000000905"
	attachmentTwoID := "00000000-0000-4000-8000-000000000906"
	clearID := "00000000-0000-4000-8000-000000000907"

	var afterAttachment, afterText, afterSecondAttachment, afterClear identity.PasteAggregate
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, createdAt); err != nil {
			return err
		}
		if err := tx.InsertPaste(ctx, workspaceID, pasteID, createdAt); err != nil {
			return err
		}
		if _, err := tx.AppendTextRevision(
			ctx, workspaceID, pasteID, textOneID, identity.RevisionContent, "paste.created",
			aggregateEnvelope(0x11), createdAt, createdAt.AddDate(1, 0, 0),
		); err != nil {
			return err
		}
		attachmentOneAt := createdAt.Add(time.Minute)
		if _, err := tx.AppendAttachmentRevision(
			ctx, workspaceID, pasteID, attachmentOneID, "paste.revised",
			[]identity.ImageAsset{aggregateAsset(0, "aggregate/one", attachmentOneAt.Add(identity.ImageLifetime))},
			attachmentOneAt, attachmentOneAt.Add(identity.ImageLifetime),
		); err != nil {
			return err
		}
		var err error
		afterAttachment, err = tx.PasteAggregate(ctx, workspaceID, pasteID, attachmentOneAt.Add(time.Second))
		if err != nil {
			return err
		}

		textTwoAt := createdAt.Add(2 * time.Minute)
		if _, err := tx.AppendTextRevision(
			ctx, workspaceID, pasteID, textTwoID, identity.RevisionContent, "paste.revised",
			aggregateEnvelope(0x22), textTwoAt, textTwoAt.AddDate(1, 0, 0),
		); err != nil {
			return err
		}
		afterText, err = tx.PasteAggregate(ctx, workspaceID, pasteID, textTwoAt.Add(time.Second))
		if err != nil {
			return err
		}

		attachmentTwoAt := createdAt.Add(3 * time.Minute)
		if _, err := tx.AppendAttachmentRevision(
			ctx, workspaceID, pasteID, attachmentTwoID, "paste.revised",
			[]identity.ImageAsset{aggregateAsset(0, "aggregate/two", attachmentTwoAt.Add(identity.ImageLifetime))},
			attachmentTwoAt, attachmentTwoAt.Add(identity.ImageLifetime),
		); err != nil {
			return err
		}
		afterSecondAttachment, err = tx.PasteAggregate(ctx, workspaceID, pasteID, attachmentTwoAt.Add(time.Second))
		if err != nil {
			return err
		}

		clearAt := createdAt.Add(4 * time.Minute)
		if _, err := tx.AppendAttachmentRevision(
			ctx, workspaceID, pasteID, clearID, "paste.revised", nil,
			clearAt, clearAt.Add(identity.ImageLifetime),
		); err != nil {
			return err
		}
		afterClear, err = tx.PasteAggregate(ctx, workspaceID, pasteID, clearAt.Add(time.Second))
		return err
	})
	if err != nil {
		t.Fatalf("aggregate component transaction: %v", err)
	}

	assertAggregateComponents(t, afterAttachment, textOneID, attachmentOneID)
	if afterAttachment.RevisionID != attachmentOneID || afterAttachment.ServerSequence != 2 || !afterAttachment.CreatedAt.Equal(createdAt.Add(time.Minute)) {
		t.Fatalf("attachment-newer aggregate metadata = %#v", afterAttachment)
	}
	if got := afterAttachment.AttachmentRevision.Assets; len(got) != 1 || got[0].AssetIndex != 0 {
		t.Fatalf("first attachment assets = %#v", got)
	}

	assertAggregateComponents(t, afterText, textTwoID, attachmentOneID)
	if afterText.RevisionID != textTwoID || afterText.ServerSequence != 3 || !afterText.CreatedAt.Equal(createdAt.Add(2*time.Minute)) {
		t.Fatalf("text-newer aggregate metadata = %#v", afterText)
	}

	assertAggregateComponents(t, afterSecondAttachment, textTwoID, attachmentTwoID)
	if afterSecondAttachment.RevisionID != attachmentTwoID || afterSecondAttachment.ServerSequence != 4 {
		t.Fatalf("second attachment aggregate metadata = %#v", afterSecondAttachment)
	}

	assertAggregateComponents(t, afterClear, textTwoID, clearID)
	if afterClear.RevisionID != clearID || afterClear.ServerSequence != 5 {
		t.Fatalf("clear aggregate metadata = %#v", afterClear)
	}
	if afterClear.AttachmentRevision.Assets == nil || len(afterClear.AttachmentRevision.Assets) != 0 {
		t.Fatalf("clear attachment assets = %#v, want explicit empty slice", afterClear.AttachmentRevision.Assets)
	}
}

func TestPasteAggregateTombstoneOverridesAndIsExcludedFromCollections(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	createdAt := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000911"
	pasteID := "00000000-0000-4000-8000-000000000912"
	tombstoneID := "00000000-0000-4000-8000-000000000915"

	var direct identity.PasteAggregate
	var listed, snapshot []identity.PasteAggregate
	var cursor int64
	var latestErr error
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, createdAt); err != nil {
			return err
		}
		if err := tx.InsertPaste(ctx, workspaceID, pasteID, createdAt); err != nil {
			return err
		}
		if _, err := tx.AppendTextRevision(
			ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000000913",
			identity.RevisionContent, "paste.created", aggregateEnvelope(0x31),
			createdAt, createdAt.AddDate(1, 0, 0),
		); err != nil {
			return err
		}
		attachmentAt := createdAt.Add(time.Minute)
		if _, err := tx.AppendAttachmentRevision(
			ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000000914", "paste.revised",
			[]identity.ImageAsset{aggregateAsset(0, "aggregate/tombstoned", attachmentAt.Add(identity.ImageLifetime))},
			attachmentAt, attachmentAt.Add(identity.ImageLifetime),
		); err != nil {
			return err
		}
		tombstoneAt := createdAt.Add(2 * time.Minute)
		if _, err := tx.AppendTextRevision(
			ctx, workspaceID, pasteID, tombstoneID, identity.RevisionTombstone, "paste.deleted",
			secure.Envelope{}, tombstoneAt, tombstoneAt.AddDate(1, 0, 0),
		); err != nil {
			return err
		}
		var err error
		direct, err = tx.PasteAggregate(ctx, workspaceID, pasteID, tombstoneAt.Add(time.Second))
		if err != nil {
			return err
		}
		listed, err = tx.ListPasteAggregates(ctx, workspaceID, time.Unix(0, 0).UTC(), tombstoneAt.Add(time.Second))
		if err != nil {
			return err
		}
		cursor, snapshot, err = tx.SnapshotAggregates(ctx, workspaceID, tombstoneAt.Add(time.Second))
		if err != nil {
			return err
		}
		_, latestErr = tx.LatestPasteAggregate(ctx, workspaceID, tombstoneAt.Add(time.Second))
		return nil
	})
	if err != nil {
		t.Fatalf("tombstone aggregate transaction: %v", err)
	}
	if !direct.Deleted || direct.RevisionID != tombstoneID || direct.ServerSequence != 3 || direct.TextRevision != nil || direct.AttachmentRevision != nil || direct.AttachmentRevisionID != "" {
		t.Fatalf("direct tombstone aggregate = %#v", direct)
	}
	if len(listed) != 0 || len(snapshot) != 0 || cursor != 3 {
		t.Fatalf("tombstone collections = list:%#v snapshot:%#v cursor:%d", listed, snapshot, cursor)
	}
	if !errors.Is(latestErr, identity.ErrNotFound) {
		t.Fatalf("LatestPasteAggregate() tombstone error = %v", latestErr)
	}
}

func TestPasteAggregateExpiredAttachmentsDisappearWithoutHidingText(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	createdAt := time.Date(2026, 8, 12, 3, 0, 0, 0, time.UTC)
	now := createdAt.Add(25 * time.Hour)
	workspaceID := "00000000-0000-4000-8000-000000000921"
	textPasteID := "00000000-0000-4000-8000-000000000922"
	attachmentOnlyPasteID := "00000000-0000-4000-8000-000000000925"

	var aggregate identity.PasteAggregate
	var expiredOnlyErr error
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, createdAt); err != nil {
			return err
		}
		if err := tx.InsertPaste(ctx, workspaceID, textPasteID, createdAt); err != nil {
			return err
		}
		if _, err := tx.AppendTextRevision(
			ctx, workspaceID, textPasteID, "00000000-0000-4000-8000-000000000923",
			identity.RevisionContent, "paste.created", aggregateEnvelope(0x41),
			createdAt, createdAt.AddDate(1, 0, 0),
		); err != nil {
			return err
		}
		attachmentAt := createdAt.Add(time.Minute)
		if _, err := tx.AppendAttachmentRevision(
			ctx, workspaceID, textPasteID, "00000000-0000-4000-8000-000000000924", "paste.revised",
			[]identity.ImageAsset{aggregateAsset(0, "aggregate/expired", attachmentAt.Add(identity.ImageLifetime))},
			attachmentAt, attachmentAt.Add(identity.ImageLifetime),
		); err != nil {
			return err
		}
		var err error
		aggregate, err = tx.PasteAggregate(ctx, workspaceID, textPasteID, now)
		if err != nil {
			return err
		}

		expiredTextAt := now.AddDate(-1, 0, 0)
		if err := tx.InsertPaste(ctx, workspaceID, attachmentOnlyPasteID, expiredTextAt); err != nil {
			return err
		}
		if _, err := tx.AppendTextRevision(
			ctx, workspaceID, attachmentOnlyPasteID, "00000000-0000-4000-8000-000000000927",
			identity.RevisionContent, "paste.created", aggregateEnvelope(0x42),
			expiredTextAt, expiredTextAt.AddDate(1, 0, 0),
		); err != nil {
			return err
		}
		if _, err := tx.AppendAttachmentRevision(
			ctx, workspaceID, attachmentOnlyPasteID, "00000000-0000-4000-8000-000000000926", "paste.revised", nil,
			createdAt, createdAt.Add(identity.ImageLifetime),
		); err != nil {
			return err
		}
		_, expiredOnlyErr = tx.PasteAggregate(ctx, workspaceID, attachmentOnlyPasteID, now)
		return nil
	})
	if err != nil {
		t.Fatalf("expired attachment transaction: %v", err)
	}
	if aggregate.TextRevision == nil || aggregate.AttachmentRevision != nil || aggregate.AttachmentRevisionID != "" || aggregate.RevisionID != aggregate.TextRevision.RevisionID || aggregate.ServerSequence != aggregate.TextRevision.ServerSequence {
		t.Fatalf("aggregate after attachment expiry = %#v", aggregate)
	}
	if !errors.Is(expiredOnlyErr, identity.ErrNotFound) {
		t.Fatalf("expired-only PasteAggregate() error = %v", expiredOnlyErr)
	}
}

func TestPasteAggregateSurfacesLegacyImageBundleIndexesEightThroughNineteen(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	createdAt := time.Date(2026, 8, 14, 4, 0, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000931"
	pasteID := "00000000-0000-4000-8000-000000000932"
	legacyRevisionID := "00000000-0000-4000-8000-000000000934"
	legacyAt := createdAt.Add(time.Minute)
	assets := make([]identity.ImageAsset, 0, 12)
	for index := 8; index < 20; index++ {
		assets = append(assets, aggregateAsset(index, "aggregate/legacy/"+string(rune('a'+index-8)), legacyAt.Add(identity.ImageLifetime)))
	}

	var aggregate identity.PasteAggregate
	var current identity.ImageAsset
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, createdAt); err != nil {
			return err
		}
		if err := tx.InsertPaste(ctx, workspaceID, pasteID, createdAt); err != nil {
			return err
		}
		if _, err := tx.AppendTextRevision(
			ctx, workspaceID, pasteID, "00000000-0000-4000-8000-000000000933",
			identity.RevisionContent, "paste.created", aggregateEnvelope(0x51),
			createdAt, createdAt.AddDate(1, 0, 0),
		); err != nil {
			return err
		}
		if _, err := tx.AppendImageRevision(
			ctx, workspaceID, pasteID, legacyRevisionID, "paste.revised", assets,
			legacyAt, legacyAt.Add(identity.ImageLifetime),
		); err != nil {
			return err
		}
		var err error
		aggregate, err = tx.PasteAggregate(ctx, workspaceID, pasteID, legacyAt.Add(time.Second))
		if err != nil {
			return err
		}
		current, err = tx.CurrentAttachmentAsset(ctx, workspaceID, pasteID, 19, legacyAt.Add(time.Second))
		return err
	})
	if err != nil {
		t.Fatalf("legacy aggregate transaction: %v", err)
	}
	if aggregate.AttachmentRevisionID != legacyRevisionID || aggregate.AttachmentRevision == nil || aggregate.AttachmentRevision.RevisionKind != identity.RevisionImageBundle {
		t.Fatalf("legacy attachment component = %#v", aggregate)
	}
	if got := aggregate.AttachmentRevision.Assets; len(got) != 12 || got[0].AssetIndex != 8 || got[11].AssetIndex != 19 {
		t.Fatalf("legacy attachment indexes = %#v", got)
	}
	if current.AssetIndex != 19 || current.RevisionID != legacyRevisionID {
		t.Fatalf("legacy current asset = %#v", current)
	}
}

func TestPasteAggregateCurrentAssetIsPasteScopedAndClearMasksOlderBundle(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	createdAt := time.Date(2026, 8, 14, 5, 0, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000941"
	pasteOneID := "00000000-0000-4000-8000-000000000942"
	pasteTwoID := "00000000-0000-4000-8000-000000000943"

	var beforeClear, otherPaste identity.ImageAsset
	var afterClearErr error
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, createdAt); err != nil {
			return err
		}
		for _, paste := range []struct {
			pasteID, textID, attachmentID, storageKey string
		}{
			{pasteOneID, "00000000-0000-4000-8000-000000000944", "00000000-0000-4000-8000-000000000946", "aggregate/scoped/one"},
			{pasteTwoID, "00000000-0000-4000-8000-000000000945", "00000000-0000-4000-8000-000000000947", "aggregate/scoped/two"},
		} {
			if err := tx.InsertPaste(ctx, workspaceID, paste.pasteID, createdAt); err != nil {
				return err
			}
			if _, err := tx.AppendTextRevision(
				ctx, workspaceID, paste.pasteID, paste.textID, identity.RevisionContent, "paste.created",
				aggregateEnvelope(0x61), createdAt, createdAt.AddDate(1, 0, 0),
			); err != nil {
				return err
			}
			attachmentAt := createdAt.Add(time.Minute)
			if _, err := tx.AppendAttachmentRevision(
				ctx, workspaceID, paste.pasteID, paste.attachmentID, "paste.revised",
				[]identity.ImageAsset{aggregateAsset(0, paste.storageKey, attachmentAt.Add(identity.ImageLifetime))},
				attachmentAt, attachmentAt.Add(identity.ImageLifetime),
			); err != nil {
				return err
			}
		}
		var err error
		beforeClear, err = tx.CurrentAttachmentAsset(ctx, workspaceID, pasteOneID, 0, createdAt.Add(2*time.Minute))
		if err != nil {
			return err
		}
		otherPaste, err = tx.CurrentAttachmentAsset(ctx, workspaceID, pasteTwoID, 0, createdAt.Add(2*time.Minute))
		if err != nil {
			return err
		}
		clearAt := createdAt.Add(3 * time.Minute)
		if _, err := tx.AppendAttachmentRevision(
			ctx, workspaceID, pasteOneID, "00000000-0000-4000-8000-000000000948", "paste.revised", nil,
			clearAt, clearAt.Add(identity.ImageLifetime),
		); err != nil {
			return err
		}
		_, afterClearErr = tx.CurrentAttachmentAsset(ctx, workspaceID, pasteOneID, 0, clearAt.Add(time.Second))
		return nil
	})
	if err != nil {
		t.Fatalf("current attachment asset transaction: %v", err)
	}
	if beforeClear.StorageKey != "aggregate/scoped/one" || otherPaste.StorageKey != "aggregate/scoped/two" {
		t.Fatalf("paste-scoped assets = %#v / %#v", beforeClear, otherPaste)
	}
	if !errors.Is(afterClearErr, identity.ErrNotFound) {
		t.Fatalf("CurrentAttachmentAsset() after clear error = %v", afterClearErr)
	}
}

func TestPasteAggregateCollectionsUseNewestComponentTimeAndSequence(t *testing.T) {
	ctx := context.Background()
	store := New(testdb.New(t))
	createdAt := time.Date(2026, 8, 14, 6, 0, 0, 0, time.UTC)
	workspaceID := "00000000-0000-4000-8000-000000000951"
	olderSequencePasteID := "00000000-0000-4000-8000-000000000952"
	newerSequencePasteID := "00000000-0000-4000-8000-000000000953"

	var ordered, cutoff, snapshot []identity.PasteAggregate
	var latest identity.PasteAggregate
	var cursor int64
	err := store.WithinTx(ctx, func(tx identity.TxStore) error {
		if err := tx.InsertWorkspace(ctx, workspaceID, createdAt); err != nil {
			return err
		}
		lowerSequenceAt := createdAt.Add(2 * time.Minute)
		if err := tx.InsertPaste(ctx, workspaceID, olderSequencePasteID, lowerSequenceAt); err != nil {
			return err
		}
		if _, err := tx.AppendTextRevision(
			ctx, workspaceID, olderSequencePasteID, "00000000-0000-4000-8000-000000000954",
			identity.RevisionContent, "paste.created", aggregateEnvelope(0x71),
			lowerSequenceAt, lowerSequenceAt.AddDate(1, 0, 0),
		); err != nil {
			return err
		}
		higherSequenceAt := createdAt
		if err := tx.InsertPaste(ctx, workspaceID, newerSequencePasteID, higherSequenceAt); err != nil {
			return err
		}
		if _, err := tx.AppendTextRevision(
			ctx, workspaceID, newerSequencePasteID, "00000000-0000-4000-8000-000000000955",
			identity.RevisionContent, "paste.created", aggregateEnvelope(0x72),
			higherSequenceAt, higherSequenceAt.AddDate(1, 0, 0),
		); err != nil {
			return err
		}
		var err error
		queryAt := createdAt.Add(3 * time.Minute)
		ordered, err = tx.ListPasteAggregates(ctx, workspaceID, time.Time{}, queryAt)
		if err != nil {
			return err
		}
		cutoff, err = tx.ListPasteAggregates(ctx, workspaceID, createdAt.Add(time.Minute), queryAt)
		if err != nil {
			return err
		}
		cursor, snapshot, err = tx.SnapshotAggregates(ctx, workspaceID, queryAt)
		if err != nil {
			return err
		}
		latest, err = tx.LatestPasteAggregate(ctx, workspaceID, queryAt)
		return err
	})
	if err != nil {
		t.Fatalf("aggregate collection transaction: %v", err)
	}
	if len(ordered) != 2 || ordered[0].PasteID != newerSequencePasteID || ordered[1].PasteID != olderSequencePasteID || !ordered[0].CreatedAt.Before(ordered[1].CreatedAt) {
		t.Fatalf("sequence-ordered aggregate list = %#v", ordered)
	}
	if len(cutoff) != 1 || cutoff[0].PasteID != olderSequencePasteID {
		t.Fatalf("timestamp-cutoff aggregate list = %#v", cutoff)
	}
	if cursor != 2 || len(snapshot) != 2 || snapshot[0].PasteID != newerSequencePasteID || snapshot[1].PasteID != olderSequencePasteID {
		t.Fatalf("aggregate snapshot = cursor:%d items:%#v", cursor, snapshot)
	}
	if latest.PasteID != newerSequencePasteID || latest.ServerSequence != 2 {
		t.Fatalf("latest aggregate = %#v", latest)
	}
}

func assertAggregateComponents(t *testing.T, aggregate identity.PasteAggregate, textRevisionID, attachmentRevisionID string) {
	t.Helper()
	if aggregate.Deleted || aggregate.TextRevision == nil || aggregate.TextRevision.RevisionID != textRevisionID {
		t.Fatalf("text component = %#v", aggregate)
	}
	if aggregate.AttachmentRevisionID != attachmentRevisionID || aggregate.AttachmentRevision == nil || aggregate.AttachmentRevision.RevisionID != attachmentRevisionID {
		t.Fatalf("attachment component = %#v", aggregate)
	}
	if aggregate.TextExpiresAt.IsZero() || aggregate.AttachmentExpiresAt.IsZero() {
		t.Fatalf("component expiries = %#v", aggregate)
	}
}

func aggregateEnvelope(marker byte) secure.Envelope {
	return secure.Envelope{
		KeyID:      "aggregate-key",
		Nonce:      bytes.Repeat([]byte{marker}, 12),
		Ciphertext: []byte{marker},
	}
}

func aggregateAsset(index int, storageKey string, expiresAt time.Time) identity.ImageAsset {
	return identity.ImageAsset{
		AssetIndex: index,
		MIMEType:   "image/png",
		Width:      1,
		Height:     1,
		ByteSize:   1,
		ExpiresAt:  expiresAt,
		StorageKey: storageKey,
		Envelope: secure.Envelope{
			KeyID: "aggregate-key",
			Nonce: bytes.Repeat([]byte{byte(index + 1)}, 12),
		},
	}
}
