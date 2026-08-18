package peer

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRegistryRecordNormalizesAndListDeepCopies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known-peers.json")
	registry := NewRegistry(path)
	addresses := []string{"2001:db8::2", "100.64.0.2", "100.64.0.2"}
	peer := KnownPeer{
		DeviceID:    "ABCDEFAB-CDEF-ABCD-EFAB-CDEFABCDEFAB",
		DisplayName: "Mac mini",
		Addresses:   addresses,
		LastSeenAt:  time.Date(2026, time.August, 18, 10, 11, 12, 123456789, time.FixedZone("KST", 9*60*60)),
	}

	if err := registry.Record(peer); err != nil {
		t.Fatal(err)
	}
	addresses[0] = "192.0.2.99"

	got := registry.List()
	wantTime := peer.LastSeenAt.UTC()
	want := []KnownPeer{{
		DeviceID:    "abcdefab-cdef-abcd-efab-cdefabcdefab",
		DisplayName: "Mac mini",
		Addresses:   []string{"100.64.0.2", "2001:db8::2"},
		LastSeenAt:  wantTime,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %#v, want %#v", got, want)
	}
	got[0].Addresses[0] = "192.0.2.1"
	if again := registry.List(); again[0].Addresses[0] != "100.64.0.2" {
		t.Fatalf("List() did not deep-copy addresses: %#v", again)
	}
}

func TestRegistryRecordReplacesByDeviceIDAndListIsDeterministic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known-peers.json")
	registry := NewRegistry(path)
	for _, peer := range []KnownPeer{
		{DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", DisplayName: "B", Addresses: []string{"100.64.0.2"}, LastSeenAt: time.Unix(2, 0)},
		{DeviceID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", DisplayName: "A", Addresses: []string{"100.64.0.1"}, LastSeenAt: time.Unix(1, 0)},
	} {
		if err := registry.Record(peer); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Record(KnownPeer{
		DeviceID:    "BBBBBBBB-BBBB-BBBB-BBBB-BBBBBBBBBBBB",
		DisplayName: "B2",
		Addresses:   []string{"100.64.0.22"},
		LastSeenAt:  time.Unix(3, 0),
	}); err != nil {
		t.Fatal(err)
	}

	want := []KnownPeer{
		{DeviceID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", DisplayName: "A", Addresses: []string{"100.64.0.1"}, LastSeenAt: time.Unix(1, 0).UTC()},
		{DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", DisplayName: "B2", Addresses: []string{"100.64.0.22"}, LastSeenAt: time.Unix(3, 0).UTC()},
	}
	if got := registry.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %#v, want %#v", got, want)
	}
}

func TestRegistryLoadMissingFileIsEmptySuccess(t *testing.T) {
	registry := NewRegistry(filepath.Join(t.TempDir(), "missing.json"))
	if err := registry.Load(); err != nil {
		t.Fatal(err)
	}
	if got := registry.List(); len(got) != 0 {
		t.Fatalf("List() = %#v, want empty", got)
	}
}

func TestRegistryLoadReplacesStateOnlyAfterFullValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known-peers.json")
	registry := NewRegistry(path)
	initial := KnownPeer{DeviceID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", DisplayName: "Initial", Addresses: []string{"100.64.0.1"}, LastSeenAt: time.Unix(1, 0)}
	if err := registry.Record(initial); err != nil {
		t.Fatal(err)
	}

	valid := []KnownPeer{{DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", DisplayName: "Loaded", Addresses: []string{"100.64.0.2"}, LastSeenAt: time.Unix(2, 0)}}
	writeRegistryJSON(t, path, valid)
	if err := registry.Load(); err != nil {
		t.Fatal(err)
	}
	if got := registry.List(); !reflect.DeepEqual(got, []KnownPeer{{
		DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", DisplayName: "Loaded", Addresses: []string{"100.64.0.2"}, LastSeenAt: time.Unix(2, 0).UTC(),
	}}) {
		t.Fatalf("List() after valid Load = %#v, want normalized loaded peer", got)
	}

	writeRegistryRaw(t, path, `[{
		"device_id":"cccccccc-cccc-cccc-cccc-cccccccccccc",
		"display_name":"valid",
		"addresses":["100.64.0.3"],
		"last_seen_at":"2026-08-18T00:00:00Z"
	}, {
		"device_id":"CCCCCCCC-CCCC-CCCC-CCCC-CCCCCCCCCCCC",
		"display_name":"duplicate",
		"addresses":["100.64.0.4"],
		"last_seen_at":"2026-08-18T00:00:00Z"
	}]`)
	if err := registry.Load(); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("Load() error = %v, want %v", err, ErrInvalidRegistry)
	}
	want := []KnownPeer{{DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", DisplayName: "Loaded", Addresses: []string{"100.64.0.2"}, LastSeenAt: time.Unix(2, 0).UTC()}}
	if got := registry.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("List() after rejected Load = %#v, want %#v", got, want)
	}
}

func TestRegistryRejectsInvalidRecordsWithoutChangingPersistedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known-peers.json")
	registry := NewRegistry(path)
	initial := KnownPeer{DeviceID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", DisplayName: "Initial", Addresses: []string{"100.64.0.1"}, LastSeenAt: time.Unix(1, 0)}
	if err := registry.Record(initial); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	invalid := []KnownPeer{
		{DeviceID: "not-a-uuid", DisplayName: "Name", Addresses: []string{"100.64.0.2"}, LastSeenAt: time.Unix(2, 0)},
		{DeviceID: "00000000-0000-0000-0000-000000000000", DisplayName: "Name", Addresses: []string{"100.64.0.2"}, LastSeenAt: time.Unix(2, 0)},
		{DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", DisplayName: "", Addresses: []string{"100.64.0.2"}, LastSeenAt: time.Unix(2, 0)},
		{DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", DisplayName: "bad\nname", Addresses: []string{"100.64.0.2"}, LastSeenAt: time.Unix(2, 0)},
		{DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", DisplayName: string([]byte{0xff}), Addresses: []string{"100.64.0.2"}, LastSeenAt: time.Unix(2, 0)},
		{DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", DisplayName: "Name", Addresses: nil, LastSeenAt: time.Unix(2, 0)},
		{DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", DisplayName: "Name", Addresses: []string{"not-an-ip"}, LastSeenAt: time.Unix(2, 0)},
		{DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", DisplayName: "Name", Addresses: []string{"100.64.0.2"}, LastSeenAt: time.Time{}},
	}
	for _, peer := range invalid {
		if err := registry.Record(peer); !errors.Is(err, ErrInvalidRegistry) {
			t.Fatalf("Record(%#v) error = %v, want %v", peer, err, ErrInvalidRegistry)
		}
	}
	if got := registry.List(); !reflect.DeepEqual(got, []KnownPeer{{
		DeviceID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", DisplayName: "Initial", Addresses: []string{"100.64.0.1"}, LastSeenAt: time.Unix(1, 0).UTC(),
	}}) {
		t.Fatalf("List() after invalid records = %#v", got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatal("invalid Record changed persisted state")
	}
}

func TestRegistryRejectsNilDeviceIDOnLoadWithoutChangingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known-peers.json")
	registry := NewRegistry(path)
	initial := KnownPeer{DeviceID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", DisplayName: "Initial", Addresses: []string{"100.64.0.1"}, LastSeenAt: time.Unix(1, 0)}
	if err := registry.Record(initial); err != nil {
		t.Fatal(err)
	}

	writeRegistryRaw(t, path, `[{"device_id":"00000000-0000-0000-0000-000000000000","display_name":"Nil","addresses":["100.64.0.2"],"last_seen_at":"2026-08-18T00:00:00Z"}]`)
	if err := registry.Load(); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("Load() error = %v, want %v", err, ErrInvalidRegistry)
	}
	if got := registry.List(); !reflect.DeepEqual(got, []KnownPeer{{
		DeviceID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", DisplayName: "Initial", Addresses: []string{"100.64.0.1"}, LastSeenAt: time.Unix(1, 0).UTC(),
	}}) {
		t.Fatalf("List() after nil UUID Load = %#v", got)
	}
}

func TestRegistryRecordRollbackOnPersistenceFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known-peers.json")
	registry := NewRegistry(path)
	initial := KnownPeer{DeviceID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", DisplayName: "Initial", Addresses: []string{"100.64.0.1"}, LastSeenAt: time.Unix(1, 0)}
	if err := registry.Record(initial); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}

	updated := KnownPeer{DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", DisplayName: "Updated", Addresses: []string{"100.64.0.2"}, LastSeenAt: time.Unix(2, 0)}
	err := registry.Record(updated)
	if !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("Record() error = %v, want %v", err, ErrRegistryUnavailable)
	}
	if got := registry.List(); !reflect.DeepEqual(got, []KnownPeer{{
		DeviceID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", DisplayName: "Initial", Addresses: []string{"100.64.0.1"}, LastSeenAt: time.Unix(1, 0).UTC(),
	}}) {
		t.Fatalf("List() after persistence failure = %#v", got)
	}
}

func TestRegistryFileContainsOnlyApprovedKeysAndMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known-peers.json")
	registry := NewRegistry(path)
	if err := registry.Record(KnownPeer{
		DeviceID:    "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		DisplayName: "Mac mini",
		Addresses:   []string{"100.64.0.1"},
		LastSeenAt:  time.Unix(1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("registry mode = %o, want 600", got)
	}

	var records []map[string]json.RawMessage
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one record", records)
	}
	for key := range records[0] {
		if key != "device_id" && key != "display_name" && key != "addresses" && key != "last_seen_at" {
			t.Fatalf("forbidden registry key %q in %s", key, raw)
		}
	}
	for _, forbidden := range []string{"revision", "text", "image", "token", "health"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("forbidden registry content %q in %s", forbidden, raw)
		}
	}
}

func TestRegistryLoadRejectsUnknownFieldsTrailingValuesAndOversize(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "unknown field",
			raw:  `[{"device_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","display_name":"Name","addresses":["100.64.0.1"],"last_seen_at":"2026-08-18T00:00:00Z","text":"forbidden"}]`,
		},
		{
			name: "trailing value",
			raw:  `[] {"unexpected":true}`,
		},
		{
			name: "not an array",
			raw:  `null`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "known-peers.json")
			writeRegistryRaw(t, path, test.raw)
			if err := NewRegistry(path).Load(); !errors.Is(err, ErrInvalidRegistry) {
				t.Fatalf("Load() error = %v, want %v", err, ErrInvalidRegistry)
			}
		})
	}

	t.Run("oversize", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "known-peers.json")
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), (1<<20)+1), 0600); err != nil {
			t.Fatal(err)
		}
		if err := NewRegistry(path).Load(); !errors.Is(err, ErrInvalidRegistry) {
			t.Fatalf("Load() error = %v, want %v", err, ErrInvalidRegistry)
		}
	})
}

func TestRegistryLoadRejectsInvalidUUIDNameIPAndZeroTime(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value string
	}{
		{name: "uuid", field: "device_id", value: "not-a-uuid"},
		{name: "name", field: "display_name", value: "bad\u0000name"},
		{name: "address", field: "addresses", value: "not-an-ip"},
		{name: "time", field: "last_seen_at", value: "0001-01-01T00:00:00Z"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "known-peers.json")
			raw := `[{"device_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","display_name":"Name","addresses":["100.64.0.1"],"last_seen_at":"2026-08-18T00:00:00Z"}]`
			switch test.field {
			case "device_id":
				raw = strings.Replace(raw, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", test.value, 1)
			case "display_name":
				raw = strings.Replace(raw, `"Name"`, `"`+test.value+`"`, 1)
			case "addresses":
				raw = strings.Replace(raw, `"100.64.0.1"`, `"`+test.value+`"`, 1)
			case "last_seen_at":
				raw = strings.Replace(raw, "2026-08-18T00:00:00Z", test.value, 1)
			}
			writeRegistryRaw(t, path, raw)
			if err := NewRegistry(path).Load(); !errors.Is(err, ErrInvalidRegistry) {
				t.Fatalf("Load() error = %v, want %v", err, ErrInvalidRegistry)
			}
		})
	}
}

func TestRegistryRejectsSymlinkAtTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known-peers.json")
	target := filepath.Join(dir, "target.json")
	writeRegistryJSON(t, target, []KnownPeer{{DeviceID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", DisplayName: "Target", Addresses: []string{"100.64.0.1"}, LastSeenAt: time.Unix(1, 0)}})
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(path)
	if err := registry.Load(); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("Load() error = %v, want %v", err, ErrInvalidRegistry)
	}
	if err := registry.Record(KnownPeer{DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", DisplayName: "Nope", Addresses: []string{"100.64.0.2"}, LastSeenAt: time.Unix(2, 0)}); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("Record() error = %v, want %v", err, ErrInvalidRegistry)
	}
}

func TestOpenRegistryNoFollowRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	path := filepath.Join(dir, "known-peers.json")
	writeRegistryRaw(t, target, "[]")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	file, err := openRegistryNoFollow(path)
	if file != nil {
		_ = file.Close()
	}
	if !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("openRegistryNoFollow() error = %v, want %v", err, ErrInvalidRegistry)
	}
	if strings.Contains(err.Error(), path) {
		t.Fatalf("openRegistryNoFollow() leaked path: %v", err)
	}
}

func TestRegistryRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known-peers.json")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}

	loadResult := make(chan error, 1)
	go func() {
		loadResult <- NewRegistry(path).Load()
	}()
	select {
	case err := <-loadResult:
		if !errors.Is(err, ErrInvalidRegistry) {
			t.Fatalf("Load() error = %v, want %v", err, ErrInvalidRegistry)
		}
	case <-time.After(time.Second):
		t.Fatal("Load() blocked on FIFO registry target")
	}

	file, err := openRegistryNoFollow(path)
	if file != nil {
		_ = file.Close()
	}
	if !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("openRegistryNoFollow() error = %v, want %v", err, ErrInvalidRegistry)
	}
}

func TestCommitRegistryFileTreatsRenameAsCommitPoint(t *testing.T) {
	dir := t.TempDir()
	temporary := filepath.Join(dir, ".known-peers.json.tmp")
	target := filepath.Join(dir, "known-peers.json")
	if err := os.WriteFile(temporary, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := commitRegistryFile(temporary, target); err != nil {
		t.Fatalf("commitRegistryFile() error = %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("committed target = %q, want new", data)
	}
	if _, err := os.Stat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file stat error = %v, want not-exist", err)
	}
}

func writeRegistryJSON(t *testing.T, path string, peers []KnownPeer) {
	t.Helper()
	raw, err := json.Marshal(peers)
	if err != nil {
		t.Fatal(err)
	}
	writeRegistryRaw(t, path, string(raw))
}

func writeRegistryRaw(t *testing.T, path, raw string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
}
