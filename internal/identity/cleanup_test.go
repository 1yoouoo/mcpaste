package identity_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

var errCleanupTreeRemoval = errors.New("cleanup tree removal failed")

type cleanupOrderStore struct {
	identity.Store
	events         *[]string
	expired        []identity.ExpiredImageRevision
	purgeCalls     int
	purged         []identity.ExpiredImageRevision
	observedTimes  []time.Time
	observedLimits []int
}

func (s *cleanupOrderStore) Cleanup(_ context.Context, now time.Time) (identity.CleanupResult, error) {
	*s.events = append(*s.events, "metadata")
	s.observedTimes = append(s.observedTimes, now)
	return identity.CleanupResult{PairingRows: 1}, nil
}

func (s *cleanupOrderStore) PurgeText(_ context.Context, now time.Time) (int64, int64, error) {
	*s.events = append(*s.events, "text")
	s.observedTimes = append(s.observedTimes, now)
	return 2, 3, nil
}

func (s *cleanupOrderStore) ListExpiredImageRevisions(_ context.Context, now time.Time, limit int) ([]identity.ExpiredImageRevision, error) {
	*s.events = append(*s.events, "list")
	s.observedTimes = append(s.observedTimes, now)
	s.observedLimits = append(s.observedLimits, limit)
	return s.expired, nil
}

func (s *cleanupOrderStore) PurgeImageRevisions(_ context.Context, now time.Time, expired []identity.ExpiredImageRevision) (int64, int64, error) {
	*s.events = append(*s.events, "purge")
	s.observedTimes = append(s.observedTimes, now)
	s.purgeCalls++
	s.purged = append([]identity.ExpiredImageRevision(nil), expired...)
	return int64(len(expired)), 4, nil
}

type cleanupOrderImageStore struct {
	identity.ImageStore
	events  *[]string
	call    int
	failAt  int
	removed []identity.ExpiredImageRevision
}

func (s *cleanupOrderImageStore) RemoveTree(workspaceID, pasteID, revisionID string) error {
	s.call++
	*s.events = append(*s.events, "remove:"+revisionID)
	s.removed = append(s.removed, identity.ExpiredImageRevision{
		WorkspaceID: workspaceID,
		PasteID:     pasteID,
		RevisionID:  revisionID,
	})
	if s.call == s.failAt {
		return errCleanupTreeRemoval
	}
	return nil
}

func TestCleanupRemovesEveryRevisionTreeInSelectionOrderBeforeDatabasePurge(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	expired := []identity.ExpiredImageRevision{
		{WorkspaceID: "workspace-1", PasteID: "paste-1", RevisionID: "revision-2"},
		{WorkspaceID: "workspace-1", PasteID: "paste-2", RevisionID: "revision-1"},
	}
	events := []string{}
	store := &cleanupOrderStore{events: &events, expired: expired}
	imageStore := &cleanupOrderImageStore{events: &events}
	service := identity.NewService(store, nil, nil, fixedClock{value: now})
	service.SetImageStore(imageStore)

	result, err := service.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if want := []string{"metadata", "text", "list", "remove:revision-2", "remove:revision-1", "purge"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("cleanup order = %#v, want %#v", events, want)
	}
	if result.PairingRows != 1 || result.TextRevisionRows != 2 || result.TextPasteRows != 3 || result.ImageRevisionRows != 2 || result.ImageAssetRows != 4 {
		t.Fatalf("Cleanup() result = %#v", result)
	}
	if store.purgeCalls != 1 || !reflect.DeepEqual(store.purged, expired) {
		t.Fatalf("PurgeImageRevisions() calls/input = %d/%#v", store.purgeCalls, store.purged)
	}
	if want := []identity.ExpiredImageRevision{
		{WorkspaceID: "workspace-1", PasteID: "paste-1", RevisionID: "revision-2"},
		{WorkspaceID: "workspace-1", PasteID: "paste-2", RevisionID: "revision-1"},
	}; !reflect.DeepEqual(imageStore.removed, want) {
		t.Fatalf("RemoveTree() calls = %#v, want %#v", imageStore.removed, want)
	}
	if !reflect.DeepEqual(store.observedTimes, []time.Time{now, now, now, now}) || !reflect.DeepEqual(store.observedLimits, []int{100}) {
		t.Fatalf("cleanup time/limits = %#v/%#v", store.observedTimes, store.observedLimits)
	}
}

func TestCleanupDoesNotPurgeDatabaseWhenAnyRevisionTreeRemovalFails(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	expired := []identity.ExpiredImageRevision{
		{WorkspaceID: "workspace-1", PasteID: "paste-1", RevisionID: "revision-1"},
		{WorkspaceID: "workspace-1", PasteID: "paste-1", RevisionID: "revision-2"},
		{WorkspaceID: "workspace-1", PasteID: "paste-1", RevisionID: "revision-3"},
	}
	events := []string{}
	store := &cleanupOrderStore{events: &events, expired: expired}
	imageStore := &cleanupOrderImageStore{events: &events, failAt: 2}
	service := identity.NewService(store, nil, nil, fixedClock{value: now})
	service.SetImageStore(imageStore)

	result, err := service.Cleanup(context.Background())
	if !errors.Is(err, errCleanupTreeRemoval) {
		t.Fatalf("Cleanup() error = %v, want tree removal failure", err)
	}
	if want := []string{"metadata", "text", "list", "remove:revision-1", "remove:revision-2"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("cleanup failure order = %#v, want %#v", events, want)
	}
	if store.purgeCalls != 0 || store.purged != nil {
		t.Fatalf("PurgeImageRevisions() calls/input = %d/%#v, want 0/nil", store.purgeCalls, store.purged)
	}
	if len(imageStore.removed) != 2 {
		t.Fatalf("RemoveTree() calls = %#v, want first two revisions only", imageStore.removed)
	}
	if result.PairingRows != 1 || result.TextRevisionRows != 2 || result.TextPasteRows != 3 || result.ImageRevisionRows != 0 || result.ImageAssetRows != 0 {
		t.Fatalf("partial Cleanup() result = %#v", result)
	}
}
