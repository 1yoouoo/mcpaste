package peer

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

func TestRevisionCompareUsesWallLogicalThenDevice(t *testing.T) {
	a := Revision{WallMillis: 10, Logical: 1, DeviceID: "a"}
	checks := []struct {
		other Revision
		want  int
	}{
		{Revision{WallMillis: 11, Logical: 0, DeviceID: "a"}, -1},
		{Revision{WallMillis: 10, Logical: 2, DeviceID: "a"}, -1},
		{Revision{WallMillis: 10, Logical: 1, DeviceID: "b"}, -1},
		{a, 0},
	}
	for _, check := range checks {
		if got := a.Compare(check.other); got != check.want {
			t.Fatalf("Compare(%+v) = %d, want %d", check.other, got, check.want)
		}
	}
}

func TestClockTicksAfterObservedRemoteRevision(t *testing.T) {
	now := func() time.Time { return time.UnixMilli(100) }
	clock := NewClock("local", now)
	clock.Observe(Revision{WallMillis: 120, Logical: 4, DeviceID: "remote"})
	got := clock.Tick()
	want := Revision{WallMillis: 120, Logical: 5, DeviceID: "local"}
	if got != want {
		t.Fatalf("Tick() = %+v, want %+v", got, want)
	}
}

func TestClockTickCarriesLogicalOverflowIntoNextMillisecond(t *testing.T) {
	clock := NewClock("local", func() time.Time { return time.UnixMilli(500) })
	clock.wallMillis = 500
	clock.logical = math.MaxUint32

	got := clock.Tick()
	want := Revision{WallMillis: 501, Logical: 0, DeviceID: "local"}
	if got != want {
		t.Fatalf("Tick() = %+v, want %+v", got, want)
	}
}

func TestClockTickPanicsWhenNowUsesReservedWall(t *testing.T) {
	clock := NewClock("local", func() time.Time { return time.UnixMilli(math.MaxInt64) })

	assertPanics(t, func() { clock.Tick() })
	if clock.wallMillis != 0 || clock.logical != 0 {
		t.Fatalf("reserved now mutated clock to wall=%d logical=%d", clock.wallMillis, clock.logical)
	}
}

func TestClockTickPanicsWhenCarryWouldEnterReservedWall(t *testing.T) {
	clock := NewClock("local", func() time.Time { return time.UnixMilli(0) })
	clock.wallMillis = math.MaxInt64 - 1
	clock.logical = math.MaxUint32

	assertPanics(t, func() { clock.Tick() })
	if clock.wallMillis != math.MaxInt64-1 || clock.logical != math.MaxUint32 {
		t.Fatalf("reserved carry mutated clock to wall=%d logical=%d", clock.wallMillis, clock.logical)
	}
}

func TestClockTryTickReturnsExhaustedWithoutMutation(t *testing.T) {
	tests := []struct {
		name       string
		wallMillis int64
		logical    uint32
		nowMillis  int64
	}{
		{name: "reserved wall", wallMillis: math.MaxInt64, logical: 7, nowMillis: 100},
		{name: "reserved carry", wallMillis: math.MaxInt64 - 1, logical: math.MaxUint32, nowMillis: 100},
		{name: "reserved now", wallMillis: 50, logical: 2, nowMillis: math.MaxInt64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := NewClock("local", func() time.Time { return time.UnixMilli(test.nowMillis) })
			clock.wallMillis = test.wallMillis
			clock.logical = test.logical

			got, err := clock.TryTick()
			if !errors.Is(err, ErrClockExhausted) {
				t.Fatalf("TryTick() error = %v, want %v", err, ErrClockExhausted)
			}
			if got != (Revision{}) {
				t.Fatalf("TryTick() revision = %+v, want zero revision", got)
			}
			if clock.wallMillis != test.wallMillis || clock.logical != test.logical {
				t.Fatalf("TryTick() mutated clock to wall=%d logical=%d", clock.wallMillis, clock.logical)
			}
		})
	}
}

func TestClockRejectsReservedRemoteWallWithoutMutation(t *testing.T) {
	for _, logical := range []uint32{0, 1, math.MaxUint32} {
		t.Run(fmt.Sprintf("logical_%d", logical), func(t *testing.T) {
			clock := NewClock("local", func() time.Time { return time.UnixMilli(77) })
			remote := Revision{WallMillis: math.MaxInt64, Logical: logical, DeviceID: "attacker"}

			if clock.Observe(remote) {
				t.Fatal("Observe(reserved remote wall) = true, want false")
			}
			if clock.wallMillis != 0 || clock.logical != 0 {
				t.Fatalf("reserved remote mutated clock to wall=%d logical=%d", clock.wallMillis, clock.logical)
			}

			got := clock.Tick()
			want := Revision{WallMillis: 77, Logical: 0, DeviceID: "local"}
			if got != want {
				t.Fatalf("Tick() after rejected remote = %+v, want %+v", got, want)
			}
		})
	}
}

func TestClockObserveRejectsReservedNowWithoutMutation(t *testing.T) {
	clock := NewClock("local", func() time.Time { return time.UnixMilli(math.MaxInt64) })
	clock.wallMillis = 50
	clock.logical = 2
	remote := Revision{WallMillis: 100, Logical: 3, DeviceID: "remote"}

	if clock.Observe(remote) {
		t.Fatal("Observe(normal remote with reserved now) = true, want false")
	}
	if clock.wallMillis != 50 || clock.logical != 2 {
		t.Fatalf("reserved now mutated clock to wall=%d logical=%d", clock.wallMillis, clock.logical)
	}
}

func TestClockTickCarriesAfterObservedMaximumLogicalRevision(t *testing.T) {
	clock := NewClock("local", func() time.Time { return time.UnixMilli(100) })
	if !clock.Observe(Revision{WallMillis: 100, Logical: math.MaxUint32, DeviceID: "remote"}) {
		t.Fatal("Observe(nonterminal maximum logical revision) = false, want true")
	}

	got := clock.Tick()
	want := Revision{WallMillis: 101, Logical: 0, DeviceID: "local"}
	if got != want {
		t.Fatalf("Tick() = %+v, want %+v", got, want)
	}
}

func TestClockSuccessfulTicksCanBeObservedByAnotherClock(t *testing.T) {
	tests := []struct {
		name    string
		now     int64
		prepare func(*Clock)
		want    Revision
	}{
		{
			name: "normal",
			now:  100,
			want: Revision{WallMillis: 100, Logical: 0, DeviceID: "sender"},
		},
		{
			name: "highest_allowed",
			now:  0,
			prepare: func(clock *Clock) {
				clock.wallMillis = math.MaxInt64 - 1
				clock.logical = math.MaxUint32 - 1
			},
			want: Revision{WallMillis: math.MaxInt64 - 1, Logical: math.MaxUint32, DeviceID: "sender"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sender := NewClock("sender", func() time.Time { return time.UnixMilli(test.now) })
			if test.prepare != nil {
				test.prepare(sender)
			}

			revision := sender.Tick()
			if revision != test.want {
				t.Fatalf("sender Tick() = %+v, want %+v", revision, test.want)
			}
			if revision.WallMillis == math.MaxInt64 {
				t.Fatalf("sender Tick() returned reserved wall: %+v", revision)
			}

			receiver := NewClock("receiver", func() time.Time { return time.UnixMilli(0) })
			if !receiver.Observe(revision) {
				t.Fatalf("receiver rejected successful Tick() revision: %+v", revision)
			}
		})
	}
}

func TestClockTickPreservesWallOnClockRollback(t *testing.T) {
	nowMillis := int64(100)
	clock := NewClock("local", func() time.Time { return time.UnixMilli(nowMillis) })

	first := clock.Tick()
	nowMillis = 90
	second := clock.Tick()

	if want := (Revision{WallMillis: 100, Logical: 0, DeviceID: "local"}); first != want {
		t.Fatalf("first Tick() = %+v, want %+v", first, want)
	}
	if want := (Revision{WallMillis: 100, Logical: 1, DeviceID: "local"}); second != want {
		t.Fatalf("Tick() after rollback = %+v, want %+v", second, want)
	}
}

func TestNewClockRejectsEmptyDeviceID(t *testing.T) {
	assertPanics(t, func() { NewClock("", func() time.Time { return time.UnixMilli(1) }) })
}

func TestNewClockRejectsNilNow(t *testing.T) {
	assertPanics(t, func() { NewClock("local", nil) })
}

func TestClockConcurrentTickAndObserve(t *testing.T) {
	clock := NewClock("local", func() time.Time { return time.UnixMilli(1000) })
	remotes := []Revision{
		{WallMillis: 1000, Logical: 0, DeviceID: "remote-a"},
		{WallMillis: 1000, Logical: 3, DeviceID: "remote-b"},
		{WallMillis: 1001, Logical: 1, DeviceID: "remote-c"},
		{WallMillis: 1002, Logical: 8, DeviceID: "remote-d"},
		{WallMillis: 1003, Logical: 2, DeviceID: "remote-e"},
		{WallMillis: 1004, Logical: 5, DeviceID: "remote-f"},
	}

	const workerCount = 8
	var wg sync.WaitGroup
	var revisionsMu sync.Mutex
	var revisions []Revision
	var ticks []Revision
	start := make(chan struct{})
	for worker := 0; worker < workerCount; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			if worker%2 == 0 {
				for index := worker / 2; index < len(remotes); index += workerCount / 2 {
					remote := remotes[index]
					clock.Observe(remote)
					revisionsMu.Lock()
					revisions = append(revisions, remote)
					revisionsMu.Unlock()
				}
				return
			}
			for tick := 0; tick < 16; tick++ {
				local := clock.Tick()
				revisionsMu.Lock()
				revisions = append(revisions, local)
				ticks = append(ticks, local)
				revisionsMu.Unlock()
			}
		}(worker)
	}
	close(start)
	wg.Wait()

	final := clock.Tick()
	seenLocal := make(map[Revision]struct{})
	for _, tick := range ticks {
		if tick.DeviceID != "local" {
			t.Fatalf("Tick() revision device ID = %q, want local", tick.DeviceID)
		}
		if _, duplicate := seenLocal[tick]; duplicate {
			t.Fatalf("duplicate local tick revision: %+v", tick)
		}
		seenLocal[tick] = struct{}{}
	}
	for _, revision := range revisions {
		if final.Compare(revision) <= 0 {
			t.Fatalf("final local Tick() = %+v does not outrank %+v", final, revision)
		}
	}
	if final.DeviceID != "local" {
		t.Fatalf("final Tick() device ID = %q, want local", final.DeviceID)
	}
}

func TestContextManifestJSONGolden(t *testing.T) {
	manifest := ContextManifest{
		ProtocolVersion: 1,
		Revision:        Revision{WallMillis: 1234, Logical: 2, DeviceID: "device-a"},
		SourceDeviceID:  "device-a",
		UpdatedAt:       time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC),
		Text:            "hello",
		Assets: []AssetManifest{{
			Digest:   "abc",
			MIMEType: "image/png",
			Width:    2,
			Height:   3,
			ByteSize: 4,
		}},
	}

	got, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	want := `{"protocol_version":1,"revision":{"wall_millis":1234,"logical":2,"device_id":"device-a"},"source_device_id":"device-a","updated_at":"2025-01-02T03:04:05Z","text":"hello","assets":[{"sha256":"abc","mime_type":"image/png","width":2,"height":3,"byte_size":4}]}`
	if string(got) != want {
		t.Fatalf("ContextManifest JSON = %s, want %s", got, want)
	}
}

func TestLocalContextResponseJSONGoldenFlattensManifest(t *testing.T) {
	response := LocalContextResponse{
		ContextManifest: ContextManifest{
			ProtocolVersion: 1,
			Revision:        Revision{WallMillis: 1234, Logical: 2, DeviceID: "device-a"},
			SourceDeviceID:  "device-a",
			UpdatedAt:       time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC),
			Text:            "hello",
			Assets:          []AssetManifest{},
		},
		SourceReachable: true,
		SyncState:       SyncUpToDate,
	}

	got, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	want := `{"protocol_version":1,"revision":{"wall_millis":1234,"logical":2,"device_id":"device-a"},"source_device_id":"device-a","updated_at":"2025-01-02T03:04:05Z","text":"hello","assets":[],"source_reachable":true,"sync_state":"up_to_date"}`
	if string(got) != want {
		t.Fatalf("LocalContextResponse JSON = %s, want %s", got, want)
	}
}

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("function did not panic")
		}
	}()
	fn()
}
