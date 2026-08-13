package secure

import (
	"context"
	"errors"
	"sync"

	"golang.org/x/crypto/argon2"
)

const processArgon2Capacity = 2

type argon2KeyFunc func([]byte, []byte, uint32, uint32, uint8, uint32) []byte

type argon2Limiter struct {
	slots chan struct{}
}

type RecoveryPermit struct {
	state *recoveryPermitState
}

type recoveryPermitState struct {
	limiter  *argon2Limiter
	mu       sync.Mutex
	released bool
}

var processArgon2Limiter = newArgon2Limiter(processArgon2Capacity)

var errInvalidRecoveryPermit = errors.New("recovery permit is invalid")

func newArgon2Limiter(capacity int) *argon2Limiter {
	return &argon2Limiter{slots: make(chan struct{}, capacity)}
}

func (l *argon2Limiter) acquire(ctx context.Context) (*RecoveryPermit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case l.slots <- struct{}{}:
		permit := &RecoveryPermit{state: &recoveryPermitState{limiter: l}}
		if err := ctx.Err(); err != nil {
			_ = permit.Release()
			return nil, err
		}
		return permit, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func AcquireRecoveryPermit(ctx context.Context) (*RecoveryPermit, error) {
	return processArgon2Limiter.acquire(ctx)
}

func (p *RecoveryPermit) Release() error {
	if p == nil || p.state == nil {
		return errInvalidRecoveryPermit
	}
	state := p.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.limiter == nil {
		return errInvalidRecoveryPermit
	}
	if state.released {
		return nil
	}
	state.released = true
	<-state.limiter.slots
	return nil
}

func (p *RecoveryPermit) key(
	ctx context.Context,
	password []byte,
	salt []byte,
	timeCost uint32,
	memoryKiB uint32,
	threads uint8,
	length uint32,
	derive argon2KeyFunc,
) ([]byte, error) {
	if p == nil || p.state == nil || derive == nil {
		return nil, errInvalidRecoveryPermit
	}
	state := p.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.limiter == nil || state.released {
		return nil, errInvalidRecoveryPermit
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return derive(password, salt, timeCost, memoryKiB, threads, length), nil
}

func (l *argon2Limiter) key(
	ctx context.Context,
	password []byte,
	salt []byte,
	timeCost uint32,
	memoryKiB uint32,
	threads uint8,
	length uint32,
	derive argon2KeyFunc,
) ([]byte, error) {
	permit, err := l.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer permit.Release()
	if permit.state == nil || permit.state.limiter != l {
		return nil, errInvalidRecoveryPermit
	}
	return permit.key(ctx, password, salt, timeCost, memoryKiB, threads, length, derive)
}

func recoveryKeyWithPermit(
	ctx context.Context,
	permit *RecoveryPermit,
	secret []byte,
	salt []byte,
	timeCost uint32,
	memoryKiB uint32,
	threads uint8,
	length uint32,
) ([]byte, error) {
	if permit == nil || permit.state == nil || permit.state.limiter != processArgon2Limiter {
		return nil, errInvalidRecoveryPermit
	}
	return permit.key(ctx, secret, salt, timeCost, memoryKiB, threads, length, argon2.IDKey)
}
