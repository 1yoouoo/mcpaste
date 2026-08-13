package secure

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

func TestArgon2LimiterBoundsConcurrencyCancellationAndRelease(t *testing.T) {
	limiter := processArgon2Limiter
	if capacity := cap(limiter.slots); capacity != 2 {
		t.Fatalf("limiter capacity = %d", capacity)
	}
	if occupied := len(limiter.slots); occupied != 0 {
		t.Fatalf("process limiter initially occupied = %d", occupied)
	}
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	results := make(chan error, 2)
	var active atomic.Int32
	var maximum atomic.Int32
	derive := func(_ []byte, _ []byte, _ uint32, _ uint32, _ uint8, length uint32) []byte {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return make([]byte, length)
	}
	for index := 0; index < 2; index++ {
		go func() {
			_, err := limiter.key(context.Background(), nil, nil, 1, 1, 1, 32, derive)
			results <- err
		}()
	}
	<-started
	<-started
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := limiter.key(canceled, nil, nil, 1, 1, 1, 32, derive); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled waiter did not return context cancellation")
	}
	if len(started) != 0 {
		t.Fatal("canceled waiter entered Argon2 derivation")
	}
	close(release)
	for index := 0; index < 2; index++ {
		if err := <-results; err != nil {
			t.Fatal("admitted derivation returned an error")
		}
	}
	if maximum.Load() != 2 || active.Load() != 0 || len(limiter.slots) != 0 {
		t.Fatalf("maximum/active/slots = %d/%d/%d", maximum.Load(), active.Load(), len(limiter.slots))
	}
	called := false
	if _, err := limiter.key(context.Background(), nil, nil, 1, 1, 1, 32, func(_ []byte, _ []byte, _ uint32, _ uint32, _ uint8, length uint32) []byte {
		called = true
		return make([]byte, length)
	}); err != nil || !called {
		t.Fatal("released limiter did not admit a subsequent derivation")
	}
}

func TestRecoveryPermitRejectsNilAndZeroHandles(t *testing.T) {
	code := "mcr1." + testWorkspaceID + "." + strings.Repeat("A", 22) + "." + strings.Repeat("A", 43)
	verifier := RecoveryVerifier{
		Salt:      make([]byte, 16),
		Hash:      make([]byte, 32),
		Version:   argon2.Version,
		Time:      recoveryTime,
		MemoryKiB: recoveryMemoryKiB,
		Threads:   recoveryThreads,
	}
	tests := []struct {
		name   string
		permit *RecoveryPermit
	}{
		{name: "nil handle", permit: nil},
		{name: "zero handle", permit: &RecoveryPermit{}},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			if err := item.permit.Release(); !errors.Is(err, errInvalidRecoveryPermit) {
				t.Fatal("Release did not reject invalid recovery permit")
			}
			if _, err := NewRecoveryWithPermit(context.Background(), item.permit, testWorkspaceID, nil); !errors.Is(err, errInvalidRecoveryPermit) {
				t.Fatal("NewRecoveryWithPermit did not reject invalid recovery permit")
			}
			if err := VerifyRecoveryWithPermit(context.Background(), item.permit, code, testWorkspaceID, strings.Repeat("A", 22), verifier); !errors.Is(err, errInvalidRecoveryPermit) {
				t.Fatal("VerifyRecoveryWithPermit did not reject invalid recovery permit")
			}
		})
	}
}

func TestRecoveryPermitCopiesShareStateAndReleaseOnce(t *testing.T) {
	limiter := newArgon2Limiter(1)
	permit, err := limiter.acquire(context.Background())
	if err != nil {
		t.Fatal("acquire recovery permit failed")
	}
	copied := *permit
	if copied.state == nil || copied.state != permit.state {
		t.Fatal("copied handle did not share permit state")
	}
	if err := copied.Release(); err != nil {
		t.Fatal("copied handle release failed")
	}
	if occupied := len(limiter.slots); occupied != 0 {
		t.Fatalf("slots after copied handle release = %d", occupied)
	}
	if err := permit.Release(); err != nil {
		t.Fatal("repeated release through original handle failed")
	}
	if occupied := len(limiter.slots); occupied != 0 {
		t.Fatalf("slots after original handle release = %d", occupied)
	}
	if _, err := permit.key(context.Background(), nil, nil, 1, 1, 1, 32, func(_ []byte, _ []byte, _ uint32, _ uint32, _ uint8, length uint32) []byte {
		return make([]byte, length)
	}); !errors.Is(err, errInvalidRecoveryPermit) {
		t.Fatal("released shared permit state accepted derivation")
	}
	next, err := limiter.acquire(context.Background())
	if err != nil {
		t.Fatal("slot was not reusable after shared release")
	}
	if err := next.Release(); err != nil {
		t.Fatal("release next recovery permit failed")
	}
}

func TestRecoveryPermitSerializesDerivations(t *testing.T) {
	limiter := newArgon2Limiter(1)
	permit, err := limiter.acquire(context.Background())
	if err != nil {
		t.Fatal("acquire recovery permit failed")
	}
	defer permit.Release()

	var active atomic.Int32
	var maximum atomic.Int32
	derive := func(started chan<- struct{}, finish <-chan struct{}) argon2KeyFunc {
		return func(_ []byte, _ []byte, _ uint32, _ uint32, _ uint8, length uint32) []byte {
			current := active.Add(1)
			for {
				observed := maximum.Load()
				if current <= observed || maximum.CompareAndSwap(observed, current) {
					break
				}
			}
			started <- struct{}{}
			<-finish
			active.Add(-1)
			return make([]byte, length)
		}
	}

	firstStarted := make(chan struct{}, 1)
	firstFinish := make(chan struct{})
	firstResult := make(chan error, 1)
	go func() {
		_, keyErr := permit.key(context.Background(), nil, nil, 1, 1, 1, 32, derive(firstStarted, firstFinish))
		firstResult <- keyErr
	}()
	<-firstStarted
	if permit.state.mu.TryLock() {
		permit.state.mu.Unlock()
		t.Fatal("permit mutex was not held across active derivation")
	}

	secondStarted := make(chan struct{}, 1)
	secondFinish := make(chan struct{})
	secondResult := make(chan error, 1)
	secondCalled := make(chan struct{})
	go func() {
		close(secondCalled)
		_, keyErr := permit.key(context.Background(), nil, nil, 1, 1, 1, 32, derive(secondStarted, secondFinish))
		secondResult <- keyErr
	}()
	<-secondCalled
	select {
	case <-secondStarted:
		t.Fatal("second derivation entered while first derivation was active")
	default:
	}
	close(firstFinish)
	if err := <-firstResult; err != nil {
		t.Fatal("first derivation failed")
	}
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("sequential second derivation did not start")
	}
	close(secondFinish)
	if err := <-secondResult; err != nil {
		t.Fatal("second derivation failed")
	}
	if maximum.Load() != 1 || active.Load() != 0 {
		t.Fatalf("maximum/active derivations = %d/%d", maximum.Load(), active.Load())
	}
}

func TestRecoveryPermitReleaseWaitsForActiveDerivation(t *testing.T) {
	limiter := newArgon2Limiter(1)
	permit, err := limiter.acquire(context.Background())
	if err != nil {
		t.Fatal("acquire recovery permit failed")
	}
	started := make(chan struct{})
	finish := make(chan struct{})
	deriveDone := make(chan error, 1)
	go func() {
		_, keyErr := permit.key(context.Background(), nil, nil, 1, 1, 1, 32, func(_ []byte, _ []byte, _ uint32, _ uint32, _ uint8, length uint32) []byte {
			close(started)
			<-finish
			return make([]byte, length)
		})
		deriveDone <- keyErr
	}()
	<-started
	releaseEntered := make(chan struct{})
	releaseDone := make(chan error, 1)
	go func() {
		close(releaseEntered)
		releaseDone <- permit.Release()
	}()
	<-releaseEntered
	if permit.state.mu.TryLock() {
		permit.state.mu.Unlock()
		t.Fatal("permit mutex was not held during derivation")
	}
	if occupied := len(limiter.slots); occupied != 1 {
		t.Fatalf("slot released during active derivation = %d", occupied)
	}
	select {
	case err := <-releaseDone:
		if err != nil {
			t.Fatal("Release failed during active derivation")
		}
		t.Fatal("Release returned during active derivation")
	default:
	}
	close(finish)
	if err := <-deriveDone; err != nil {
		t.Fatal("active derivation failed")
	}
	select {
	case err := <-releaseDone:
		if err != nil {
			t.Fatal("Release failed after active derivation")
		}
	case <-time.After(time.Second):
		t.Fatal("Release did not return after derivation")
	}
	if occupied := len(limiter.slots); occupied != 0 {
		t.Fatalf("slot remained occupied after Release = %d", occupied)
	}
}

func TestProductionArgon2CapacityAndSingleCallSite(t *testing.T) {
	if processArgon2Capacity != 2 || cap(processArgon2Limiter.slots) != processArgon2Capacity {
		t.Fatalf("process capacity metadata = %d/%d", processArgon2Capacity, cap(processArgon2Limiter.slots))
	}
	if argon2.Version != 0x13 {
		t.Fatalf("Argon2 version = %d", argon2.Version)
	}
}
