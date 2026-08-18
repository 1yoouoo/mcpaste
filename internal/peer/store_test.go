package peer

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewStoreRejectsInvalidConfigurationWithoutPanic(t *testing.T) {
	now := func() time.Time { return time.UnixMilli(100) }

	for _, test := range []struct {
		name     string
		deviceID string
		now      func() time.Time
	}{
		{name: "empty device ID", deviceID: "", now: now},
		{name: "nil clock", deviceID: "mac-a", now: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("NewStore panicked: %v", recovered)
				}
			}()

			if _, err := NewStore(test.deviceID, test.now); !errors.Is(err, ErrInvalidStoreConfig) {
				t.Fatalf("NewStore() error = %v, want %v", err, ErrInvalidStoreConfig)
			}
		})
	}
}

func TestStorePublishesWholeSnapshotFromStagedAssets(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	data := []byte{1, 2, 3}
	digest := sha256Hex(data)
	asset := AssetManifest{Digest: digest, MIMEType: "image/png", Width: 1, Height: 1, ByteSize: len(data)}
	if err := store.StageAsset(asset, data); err != nil {
		t.Fatal(err)
	}

	manifest, err := store.PublishLocal(LocalUpdate{Text: "exact\r\ntext  ", AssetDigests: []string{digest}})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ProtocolVersion != ProtocolVersion {
		t.Fatalf("ProtocolVersion = %d, want %d", manifest.ProtocolVersion, ProtocolVersion)
	}
	if manifest.SourceDeviceID != "mac-a" || manifest.Revision.DeviceID != "mac-a" {
		t.Fatalf("local identity = source %q, revision device %q", manifest.SourceDeviceID, manifest.Revision.DeviceID)
	}
	if manifest.Text != "exact\r\ntext  " || len(manifest.Assets) != 1 {
		t.Fatalf("manifest = %+v, want exact text and one asset", manifest)
	}
	if manifest.Assets[0] != asset {
		t.Fatalf("manifest asset = %+v, want %+v", manifest.Assets[0], asset)
	}

	view, err := store.ConnectorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if view.Manifest.Text != manifest.Text || !bytes.Equal(view.Assets[digest], data) {
		t.Fatalf("connector snapshot = %+v, assets=%v", view.Manifest, view.Assets)
	}
}

func TestStoreRejectsPartialRemoteSnapshotWithoutReplacingCurrent(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	if _, err := store.PublishLocal(LocalUpdate{Text: "local"}); err != nil {
		t.Fatal(err)
	}

	data := []byte{4, 5, 6}
	remote := ContextManifest{
		ProtocolVersion: ProtocolVersion,
		Revision:        Revision{WallMillis: 200, Logical: 0, DeviceID: "mac-b"},
		SourceDeviceID:  "mac-b",
		Text:            "remote",
		Assets:          []AssetManifest{testAsset(data, "image/jpeg")},
	}
	if err := store.AdoptRemote(remote, nil); !errors.Is(err, ErrInvalidAsset) {
		t.Fatalf("AdoptRemote() error = %v, want %v", err, ErrInvalidAsset)
	}

	manifest, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Text != "local" || manifest.Revision.DeviceID != "mac-a" {
		t.Fatalf("current manifest = %+v, want local snapshot", manifest)
	}
}

func TestStoreConnectorSnapshotRequiresCurrentSourceReachability(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	if _, err := store.PublishLocal(LocalUpdate{Text: "still available"}); err != nil {
		t.Fatal(err)
	}
	if !store.SetSourceReachable("mac-a", false) {
		t.Fatal("SetSourceReachable() = false, want true")
	}

	if _, err := store.ConnectorSnapshot(); !errors.Is(err, ErrSourceOffline) {
		t.Fatalf("ConnectorSnapshot() error = %v, want %v", err, ErrSourceOffline)
	}
	manifest, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Text != "still available" {
		t.Fatalf("Manifest() text = %q, want current text", manifest.Text)
	}
}

func TestStoreRejectsNineAssets(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	digests := make([]string, 0, MaxAssets)
	for i := 0; i < MaxAssets; i++ {
		data := []byte{byte(i + 1)}
		asset := testAsset(data, "image/png")
		if err := store.StageAsset(asset, data); err != nil {
			t.Fatal(err)
		}
		digests = append(digests, asset.Digest)
	}

	extra := []byte{MaxAssets + 1}
	if err := store.StageAsset(testAsset(extra, "image/png"), extra); !errors.Is(err, ErrInvalidAsset) || !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("StageAsset(ninth distinct asset) error = %v, want limit and invalid asset", err)
	}
	if _, err := store.PublishLocal(LocalUpdate{AssetDigests: digests}); err != nil {
		t.Fatalf("PublishLocal(previously staged assets) error = %v", err)
	}
}

func TestStoreRejectsBundleOverMaxBytes(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	digests := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		data := make([]byte, MaxAssetBytes)
		data[0] = byte(i + 1)
		asset := testAsset(data, "image/png")
		if err := store.StageAsset(asset, data); err != nil {
			t.Fatal(err)
		}
		digests = append(digests, asset.Digest)
	}

	if _, err := store.PublishLocal(LocalUpdate{Text: "x", AssetDigests: digests}); !errors.Is(err, ErrInvalidAsset) || !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("PublishLocal() error = %v, want limit and invalid asset", err)
	}
}

func TestStoreRejectsAdditionalStagedAssetAfterExactBundleCapacity(t *testing.T) {
	store, digests, _ := stageFullBundle(t)
	extraData := []byte{99}
	extra := testAsset(extraData, "image/png")

	if err := store.StageAsset(extra, extraData); !errors.Is(err, ErrInvalidAsset) || !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("StageAsset(extra) error = %v, want limit and invalid asset", err)
	}
	if _, err := store.PublishLocal(LocalUpdate{AssetDigests: digests}); err != nil {
		t.Fatalf("PublishLocal(previously staged assets) error = %v", err)
	}
}

func TestStoreRestagingSameDigestDoesNotConsumeBundleTwice(t *testing.T) {
	store, digests, assets := stageFullBundle(t)
	replacement := assets[0]
	replacement.Width = 2
	if err := store.StageAsset(replacement, make([]byte, MaxAssetBytes)); !errors.Is(err, ErrInvalidAsset) {
		t.Fatalf("StageAsset(replacement with mismatched bytes) error = %v, want %v", err, ErrInvalidAsset)
	}

	data := make([]byte, MaxAssetBytes)
	data[0] = 1
	replacement = testAsset(data, "image/png")
	replacement.Width = 2
	if replacement.Digest != digests[0] {
		t.Fatalf("replacement digest = %q, want %q", replacement.Digest, digests[0])
	}
	if err := store.StageAsset(replacement, data); err != nil {
		t.Fatalf("StageAsset(replacement) error = %v", err)
	}
	extraData := []byte{100}
	if err := store.StageAsset(testAsset(extraData, "image/png"), extraData); !errors.Is(err, ErrInvalidAsset) || !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("StageAsset(extra after replacement) error = %v, want limit and invalid asset", err)
	}

	manifest, err := store.PublishLocal(LocalUpdate{AssetDigests: digests})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Assets[0].Width != 2 {
		t.Fatalf("published replacement width = %d, want 2", manifest.Assets[0].Width)
	}
}

func TestStoreReleasesConsumedStagedAssetsAfterPublish(t *testing.T) {
	store, digests, _ := stageFullBundle(t)
	if _, err := store.PublishLocal(LocalUpdate{AssetDigests: digests}); err != nil {
		t.Fatal(err)
	}

	data := []byte{101}
	if err := store.StageAsset(testAsset(data, "image/jpeg"), data); err != nil {
		t.Fatalf("StageAsset(after full publish) error = %v", err)
	}
}

func TestStageAssetRejectsIncorrectDigest(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	asset := AssetManifest{
		Digest:   strings.Repeat("0", 64),
		MIMEType: "image/png",
		Width:    1,
		Height:   1,
		ByteSize: 3,
	}
	if err := store.StageAsset(asset, []byte{1, 2, 3}); !errors.Is(err, ErrInvalidAsset) {
		t.Fatalf("StageAsset() error = %v, want %v", err, ErrInvalidAsset)
	}
}

func TestStoreEmptyClearIsNewerAndEmpty(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	if _, err := store.PublishLocal(LocalUpdate{Text: "before"}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}

	cleared, err := store.PublishLocal(LocalUpdate{})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Text != "" || cleared.Assets == nil || len(cleared.Assets) != 0 {
		t.Fatalf("clear manifest = %+v, want empty non-nil assets", cleared)
	}
	if cleared.Revision.Compare(before.Revision) <= 0 {
		t.Fatalf("clear revision %+v did not outrank %+v", cleared.Revision, before.Revision)
	}
	view, err := store.ConnectorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if view.Manifest.Text != "" || view.Manifest.Assets == nil || view.Assets == nil || len(view.Assets) != 0 {
		t.Fatalf("clear connector snapshot = %+v, assets=%v", view.Manifest, view.Assets)
	}
}

func TestStoreIgnoresEqualAndOlderRemote(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	current, err := store.PublishLocal(LocalUpdate{Text: "current"})
	if err != nil {
		t.Fatal(err)
	}

	equal := current
	equal.Text = "equal"
	if err := store.AdoptRemote(equal, map[string][]byte{}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "current" {
		t.Fatalf("equal remote replaced current with %q", got.Text)
	}

	older := ContextManifest{
		ProtocolVersion: ProtocolVersion,
		Revision:        Revision{WallMillis: current.Revision.WallMillis - 1, Logical: 99, DeviceID: "mac-b"},
		SourceDeviceID:  "mac-b",
		Text:            "older",
		Assets:          []AssetManifest{},
	}
	if err := store.AdoptRemote(older, map[string][]byte{}); err != nil {
		t.Fatal(err)
	}
	got, err = store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "current" {
		t.Fatalf("older remote replaced current with %q", got.Text)
	}
}

func TestStoreRejectsPoisoningRevisionsWithoutClockMutation(t *testing.T) {
	var nowMillis atomic.Int64
	nowMillis.Store(100)
	store := newTestStoreWithNow(t, "mac-a", func() time.Time { return time.UnixMilli(nowMillis.Load()) })
	before, err := store.PublishLocal(LocalUpdate{Text: "before"})
	if err != nil {
		t.Fatal(err)
	}

	poison := ContextManifest{
		ProtocolVersion: ProtocolVersion,
		Revision:        Revision{WallMillis: math.MaxInt64 - 1, Logical: math.MaxUint32, DeviceID: "evil"},
		SourceDeviceID:  "evil",
		Text:            "poison",
		Assets:          []AssetManifest{},
	}
	if err := store.AdoptRemote(poison, map[string][]byte{}); !errors.Is(err, ErrInvalidRevision) {
		t.Fatalf("AdoptRemote(poison) error = %v, want %v", err, ErrInvalidRevision)
	}

	future := ContextManifest{
		ProtocolVersion: ProtocolVersion,
		Revision: Revision{
			WallMillis: nowMillis.Load() + int64((24*time.Hour)/time.Millisecond) + 1,
			Logical:    0,
			DeviceID:   "evil",
		},
		SourceDeviceID: "evil",
		Text:           "future",
		Assets:         []AssetManifest{},
	}
	if err := store.AdoptRemote(future, map[string][]byte{}); !errors.Is(err, ErrInvalidRevision) {
		t.Fatalf("AdoptRemote(future) error = %v, want %v", err, ErrInvalidRevision)
	}

	current, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if current.Text != before.Text || current.Revision != before.Revision {
		t.Fatalf("invalid remote changed current: %+v", current)
	}

	after, err := store.PublishLocal(LocalUpdate{Text: "after"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != (Revision{WallMillis: 100, Logical: 1, DeviceID: "mac-a"}) {
		t.Fatalf("post-rejection local revision = %+v, want wall=100 logical=1", after.Revision)
	}
}

func TestStorePublishLocalReturnsClockExhaustedWithoutReplacingCurrent(t *testing.T) {
	var nowMillis atomic.Int64
	nowMillis.Store(100)
	store := newTestStoreWithNow(t, "mac-a", func() time.Time { return time.UnixMilli(nowMillis.Load()) })
	before, err := store.PublishLocal(LocalUpdate{Text: "before"})
	if err != nil {
		t.Fatal(err)
	}

	nowMillis.Store(math.MaxInt64)
	if _, err := store.PublishLocal(LocalUpdate{Text: "must not publish"}); !errors.Is(err, ErrClockExhausted) {
		t.Fatalf("PublishLocal(exhausted clock) error = %v, want %v", err, ErrClockExhausted)
	}
	current, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if current.Text != before.Text || current.Revision != before.Revision {
		t.Fatalf("exhausted publish changed current: %+v", current)
	}
}

func TestStoreReachabilityIsSpecificToCurrentSource(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	if _, err := store.PublishLocal(LocalUpdate{Text: "local"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AdoptRemote(testRemoteManifest(200, "mac-b", "b"), map[string][]byte{}); err != nil {
		t.Fatal(err)
	}
	if err := store.AdoptRemote(testRemoteManifest(300, "mac-c", "c"), map[string][]byte{}); err != nil {
		t.Fatal(err)
	}

	if store.SetSourceReachable("mac-b", false) {
		t.Fatal("delayed source B update applied to current source C")
	}
	if _, err := store.ConnectorSnapshot(); err != nil {
		t.Fatalf("ConnectorSnapshot() after ignored B update error = %v", err)
	}
	if !store.SetSourceReachable("mac-c", false) {
		t.Fatal("current source C update was not applied")
	}
	if _, err := store.ConnectorSnapshot(); !errors.Is(err, ErrSourceOffline) {
		t.Fatalf("ConnectorSnapshot() after current source update error = %v, want %v", err, ErrSourceOffline)
	}
}

func TestStoreExpiresUnreferencedStagedAssetAfterThirtySeconds(t *testing.T) {
	var nowMillis atomic.Int64
	nowMillis.Store(100)
	store := newTestStoreWithNow(t, "mac-a", func() time.Time { return time.UnixMilli(nowMillis.Load()) })
	data := []byte{7, 8, 9}
	asset := testAsset(data, "image/png")
	if err := store.StageAsset(asset, data); err != nil {
		t.Fatal(err)
	}

	nowMillis.Store(31_101)
	store.SweepStaged()
	if _, err := store.PublishLocal(LocalUpdate{AssetDigests: []string{asset.Digest}}); !errors.Is(err, ErrInvalidAsset) {
		t.Fatalf("PublishLocal() after expiry error = %v, want %v", err, ErrInvalidAsset)
	}
}

func TestStoreSweepReleasesRestagedCurrentQuota(t *testing.T) {
	var nowMillis atomic.Int64
	nowMillis.Store(100)
	store := newTestStoreWithNow(t, "mac-a", func() time.Time { return time.UnixMilli(nowMillis.Load()) })
	currentData := []byte{1, 2, 3}
	currentAsset := testAsset(currentData, "image/png")
	if err := store.StageAsset(currentAsset, currentData); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishLocal(LocalUpdate{AssetDigests: []string{currentAsset.Digest}}); err != nil {
		t.Fatal(err)
	}
	if err := store.StageAsset(currentAsset, currentData); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxAssets-1; i++ {
		data := []byte{byte(i + 10)}
		if err := store.StageAsset(testAsset(data, "image/png"), data); err != nil {
			t.Fatalf("StageAsset(filler %d) error = %v", i, err)
		}
	}

	nowMillis.Store(31_101)
	store.SweepStaged()
	for i := 0; i < MaxAssets; i++ {
		data := []byte{byte(i + 100)}
		if err := store.StageAsset(testAsset(data, "image/jpeg"), data); err != nil {
			t.Fatalf("StageAsset(after sweep %d) error = %v", i, err)
		}
	}

	manifest, err := store.PublishLocal(LocalUpdate{AssetDigests: []string{currentAsset.Digest}})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Assets) != 1 || manifest.Assets[0] != currentAsset {
		t.Fatalf("current fallback after sweep = %+v, want current asset", manifest)
	}
	view, err := store.ConnectorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(view.Assets[currentAsset.Digest], currentData) {
		t.Fatalf("current bytes after sweep = %v, want %v", view.Assets[currentAsset.Digest], currentData)
	}
}

func TestStoreValidatesAssetAndTextLimits(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	data := []byte{1}
	cases := []struct {
		name  string
		asset AssetManifest
		bytes []byte
	}{
		{
			name:  "mime",
			asset: testAsset(data, "image/gif"),
			bytes: data,
		},
		{
			name:  "width",
			asset: func() AssetManifest { asset := testAsset(data, "image/png"); asset.Width = 0; return asset }(),
			bytes: data,
		},
		{
			name:  "height",
			asset: func() AssetManifest { asset := testAsset(data, "image/png"); asset.Height = 0; return asset }(),
			bytes: data,
		},
		{
			name:  "byte size",
			asset: func() AssetManifest { asset := testAsset(data, "image/png"); asset.ByteSize = 2; return asset }(),
			bytes: data,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := store.StageAsset(test.asset, test.bytes); !errors.Is(err, ErrInvalidAsset) {
				t.Fatalf("StageAsset() error = %v, want %v", err, ErrInvalidAsset)
			}
		})
	}

	large := make([]byte, MaxAssetBytes+1)
	largeAsset := testAsset(large, "image/png")
	if err := store.StageAsset(largeAsset, large); !errors.Is(err, ErrInvalidAsset) || !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("StageAsset(oversized) error = %v, want limit and invalid asset", err)
	}
	if _, err := store.PublishLocal(LocalUpdate{Text: strings.Repeat("x", MaxTextBytes+1)}); !errors.Is(err, ErrInvalidAsset) || !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("PublishLocal(oversized text) error = %v, want limit and invalid asset", err)
	}
	tooManyDigests := make([]string, MaxAssets+1)
	for index := range tooManyDigests {
		tooManyDigests[index] = strings.Repeat("a", 64)
	}
	if _, err := store.PublishLocal(LocalUpdate{AssetDigests: tooManyDigests}); !errors.Is(err, ErrInvalidAsset) || !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("PublishLocal(too many assets) error = %v, want limit and invalid asset", err)
	}
}

func TestStoreDeepCopiesInputsAndReturnedValues(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	data := []byte{1, 2, 3}
	asset := testAsset(data, "image/png")
	if err := store.StageAsset(asset, data); err != nil {
		t.Fatal(err)
	}
	data[0] = 9
	asset.Digest = strings.Repeat("f", 64)

	if _, err := store.PublishLocal(LocalUpdate{Text: "original", AssetDigests: []string{sha256Hex([]byte{1, 2, 3})}}); err != nil {
		t.Fatal(err)
	}
	manifest, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.Assets[0].Digest = "mutated"
	again, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if again.Assets[0].Digest == "mutated" {
		t.Fatal("Manifest() returned aliases to stored asset manifests")
	}

	view, err := store.ConnectorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	view.Manifest.Assets[0].Digest = "mutated-again"
	view.Assets[again.Assets[0].Digest][0] = 8
	view.Assets[again.Assets[0].Digest] = append(view.Assets[again.Assets[0].Digest], 10)
	secondView, err := store.ConnectorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if secondView.Manifest.Assets[0].Digest != again.Assets[0].Digest {
		t.Fatal("ConnectorSnapshot() returned aliased manifest")
	}
	if !bytes.Equal(secondView.Assets[again.Assets[0].Digest], []byte{1, 2, 3}) {
		t.Fatalf("ConnectorSnapshot() returned aliased bytes: %v", secondView.Assets[again.Assets[0].Digest])
	}
}

func TestStoreAdoptsRemoteByDeepCopyingCompleteSnapshot(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	data := []byte{4, 5, 6}
	asset := testAsset(data, "image/jpeg")
	remote := ContextManifest{
		ProtocolVersion: ProtocolVersion,
		Revision:        Revision{WallMillis: 200, Logical: 1, DeviceID: "mac-b"},
		SourceDeviceID:  "mac-b",
		UpdatedAt:       time.UnixMilli(200),
		Text:            "remote",
		Assets:          []AssetManifest{asset},
	}
	bytesByDigest := map[string][]byte{asset.Digest: data}
	if err := store.AdoptRemote(remote, bytesByDigest); err != nil {
		t.Fatal(err)
	}
	data[0] = 9
	remote.Assets[0].Width = 99
	bytesByDigest[asset.Digest][1] = 8
	delete(bytesByDigest, asset.Digest)

	view, err := store.ConnectorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if view.Manifest.Assets[0].Width != 1 || !bytes.Equal(view.Assets[asset.Digest], []byte{4, 5, 6}) {
		t.Fatalf("adopted snapshot changed through input mutation: %+v, %v", view.Manifest, view.Assets[asset.Digest])
	}
}

func TestStorePreservesOrderedDuplicateRemoteManifests(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	dataA := []byte{1, 2, 3}
	dataB := []byte{4, 5, 6}
	assetA := testAsset(dataA, "image/png")
	assetB := testAsset(dataB, "image/jpeg")
	remote := ContextManifest{
		ProtocolVersion: ProtocolVersion,
		Revision:        Revision{WallMillis: 200, Logical: 0, DeviceID: "mac-b"},
		SourceDeviceID:  "mac-b",
		Assets:          []AssetManifest{assetA, assetB, assetA},
	}
	if err := store.AdoptRemote(remote, map[string][]byte{assetA.Digest: dataA, assetB.Digest: dataB}); err != nil {
		t.Fatal(err)
	}
	view, err := store.ConnectorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Manifest.Assets) != 3 || view.Manifest.Assets[0] != assetA || view.Manifest.Assets[1] != assetB || view.Manifest.Assets[2] != assetA {
		t.Fatalf("duplicate manifest order = %+v", view.Manifest.Assets)
	}
	if len(view.Assets) != 2 {
		t.Fatalf("duplicate byte map length = %d, want 2", len(view.Assets))
	}
}

func TestStoreRejectsConflictingDuplicateRemoteManifestWithoutReplacement(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	if _, err := store.PublishLocal(LocalUpdate{Text: "current"}); err != nil {
		t.Fatal(err)
	}
	data := []byte{1, 2, 3}
	asset := testAsset(data, "image/png")
	conflict := asset
	conflict.Width = 2
	remote := ContextManifest{
		ProtocolVersion: ProtocolVersion,
		Revision:        Revision{WallMillis: 200, Logical: 0, DeviceID: "mac-b"},
		SourceDeviceID:  "mac-b",
		Assets:          []AssetManifest{asset, conflict},
	}
	if err := store.AdoptRemote(remote, map[string][]byte{asset.Digest: data}); !errors.Is(err, ErrInvalidAsset) {
		t.Fatalf("AdoptRemote(conflicting duplicate) error = %v, want %v", err, ErrInvalidAsset)
	}
	manifest, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Text != "current" {
		t.Fatalf("conflicting duplicate changed current to %+v", manifest)
	}
}

func TestStoreRejectsInvalidRemoteWithoutChangingCurrent(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	if _, err := store.PublishLocal(LocalUpdate{Text: "current"}); err != nil {
		t.Fatal(err)
	}
	base := ContextManifest{
		ProtocolVersion: ProtocolVersion,
		Revision:        Revision{WallMillis: 200, Logical: 0, DeviceID: "mac-b"},
		SourceDeviceID:  "mac-b",
		Text:            "remote",
		Assets:          []AssetManifest{},
	}

	cases := []struct {
		name     string
		manifest ContextManifest
		assets   map[string][]byte
		wantErr  error
	}{
		{
			name:     "protocol",
			manifest: func() ContextManifest { m := base; m.ProtocolVersion++; return m }(),
			assets:   map[string][]byte{},
			wantErr:  ErrProtocolMismatch,
		},
		{
			name:     "empty revision device",
			manifest: func() ContextManifest { m := base; m.Revision.DeviceID = ""; return m }(),
			assets:   map[string][]byte{},
			wantErr:  ErrInvalidRevision,
		},
		{
			name:     "mismatched source and revision",
			manifest: func() ContextManifest { m := base; m.SourceDeviceID = "mac-c"; return m }(),
			assets:   map[string][]byte{},
			wantErr:  ErrInvalidRevision,
		},
		{
			name:     "reserved wall",
			manifest: func() ContextManifest { m := base; m.Revision.WallMillis = math.MaxInt64; return m }(),
			assets:   map[string][]byte{},
			wantErr:  ErrInvalidRevision,
		},
		{
			name: "missing asset bytes",
			manifest: func() ContextManifest {
				m := base
				data := []byte{1}
				m.Assets = []AssetManifest{testAsset(data, "image/png")}
				return m
			}(),
			assets:  map[string][]byte{},
			wantErr: ErrInvalidAsset,
		},
		{
			name:     "extra asset bytes",
			manifest: base,
			assets:   map[string][]byte{strings.Repeat("a", 64): []byte{1}},
			wantErr:  ErrInvalidAsset,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := store.AdoptRemote(test.manifest, test.assets); !errors.Is(err, test.wantErr) {
				t.Fatalf("AdoptRemote() error = %v, want %v", err, test.wantErr)
			}
			manifest, err := store.Manifest()
			if err != nil {
				t.Fatal(err)
			}
			if manifest.Text != "current" || manifest.Revision.DeviceID != "mac-a" {
				t.Fatalf("invalid remote changed current: %+v", manifest)
			}
		})
	}
}

func TestStoreAdoptsCurrentAssetsForLaterLocalPublish(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	data := []byte{2, 4, 6}
	asset := testAsset(data, "image/png")
	remote := ContextManifest{
		ProtocolVersion: ProtocolVersion,
		Revision:        Revision{WallMillis: 200, Logical: 0, DeviceID: "mac-b"},
		SourceDeviceID:  "mac-b",
		Assets:          []AssetManifest{asset},
	}
	if err := store.AdoptRemote(remote, map[string][]byte{asset.Digest: data}); err != nil {
		t.Fatal(err)
	}

	manifest, err := store.PublishLocal(LocalUpdate{Text: "local", AssetDigests: []string{asset.Digest}})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Assets) != 1 || manifest.Assets[0] != asset {
		t.Fatalf("local publish did not resolve current asset: %+v", manifest)
	}
	view, err := store.ConnectorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(view.Assets[asset.Digest], data) {
		t.Fatalf("local snapshot lost current asset bytes: %v", view.Assets[asset.Digest])
	}
}

func TestStoreConcurrentReadersPublishersAndReachability(t *testing.T) {
	store := newTestStore(t, "mac-a", 1000)
	var wg sync.WaitGroup
	start := make(chan struct{})

	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 100; i++ {
				_, _ = store.Manifest()
				_, _ = store.ConnectorSnapshot()
			}
		}()
	}
	for worker := 0; worker < 2; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for i := 0; i < 100; i++ {
				if _, err := store.PublishLocal(LocalUpdate{Text: fmt.Sprintf("publisher-%d-%d", worker, i)}); err != nil {
					t.Errorf("PublishLocal() error = %v", err)
				}
			}
		}(worker)
	}
	for worker := 0; worker < 2; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 100; i++ {
				store.SetSourceReachable("mac-a", i%2 == 0)
			}
		}()
	}

	close(start)
	wg.Wait()
	if _, err := store.Manifest(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreReturnsNoContextUntilFirstPublish(t *testing.T) {
	store := newTestStore(t, "mac-a", 100)
	if _, err := store.Manifest(); !errors.Is(err, ErrNoContext) {
		t.Fatalf("Manifest() error = %v, want %v", err, ErrNoContext)
	}
	if _, err := store.ConnectorSnapshot(); !errors.Is(err, ErrNoContext) {
		t.Fatalf("ConnectorSnapshot() error = %v, want %v", err, ErrNoContext)
	}
}

func TestStoreSHA256HexUsesLowercaseDigest(t *testing.T) {
	if got, want := sha256Hex([]byte("abc")), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"; got != want {
		t.Fatalf("sha256Hex() = %q, want %q", got, want)
	}
}

func newTestStore(t *testing.T, deviceID string, millis int64) *Store {
	t.Helper()
	return newTestStoreWithNow(t, deviceID, func() time.Time { return time.UnixMilli(millis) })
}

func newTestStoreWithNow(t *testing.T, deviceID string, now func() time.Time) *Store {
	t.Helper()
	store, err := NewStore(deviceID, now)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store
}

func testAsset(data []byte, mime string) AssetManifest {
	return AssetManifest{
		Digest:   testDigest(data),
		MIMEType: mime,
		Width:    1,
		Height:   1,
		ByteSize: len(data),
	}
}

func testDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:])
}

func testRemoteManifest(wallMillis int64, sourceDeviceID, text string) ContextManifest {
	return ContextManifest{
		ProtocolVersion: ProtocolVersion,
		Revision:        Revision{WallMillis: wallMillis, DeviceID: sourceDeviceID},
		SourceDeviceID:  sourceDeviceID,
		Text:            text,
		Assets:          []AssetManifest{},
	}
}

func stageFullBundle(t *testing.T) (*Store, []string, []AssetManifest) {
	t.Helper()
	if MaxBundleBytes%MaxAssetBytes != 0 {
		t.Fatalf("test requires MaxBundleBytes divisible by MaxAssetBytes")
	}

	store := newTestStore(t, "mac-a", 100)
	count := MaxBundleBytes / MaxAssetBytes
	digests := make([]string, 0, count)
	assets := make([]AssetManifest, 0, count)
	for i := 0; i < count; i++ {
		data := make([]byte, MaxAssetBytes)
		data[0] = byte(i + 1)
		asset := testAsset(data, "image/png")
		if err := store.StageAsset(asset, data); err != nil {
			t.Fatalf("StageAsset(full bundle asset %d) error = %v", i, err)
		}
		digests = append(digests, asset.Digest)
		assets = append(assets, asset)
	}
	return store, digests, assets
}
