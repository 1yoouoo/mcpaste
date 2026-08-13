package secure

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"

	"golang.org/x/crypto/argon2"
)

const recoveryTime uint32 = 3
const recoveryMemoryKiB uint32 = 64 * 1024
const recoveryThreads uint8 = 4
const recoveryHashLength uint32 = 32

var ErrInvalidRecovery = errors.New("recovery code is invalid")

type RecoveryVerifier struct {
	Salt      []byte
	Hash      []byte
	Version   int
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
}

type IssuedRecovery struct {
	Code     string
	Locator  string
	Verifier RecoveryVerifier
}

func NewRecovery(ctx context.Context, workspaceID string, random Random) (IssuedRecovery, error) {
	permit, err := AcquireRecoveryPermit(ctx)
	if err != nil {
		return IssuedRecovery{}, err
	}
	defer permit.Release()
	return NewRecoveryWithPermit(ctx, permit, workspaceID, random)
}

func NewRecoveryWithPermit(ctx context.Context, permit *RecoveryPermit, workspaceID string, random Random) (IssuedRecovery, error) {
	if permit == nil || permit.state == nil || permit.state.limiter != processArgon2Limiter {
		return IssuedRecovery{}, errInvalidRecoveryPermit
	}
	if !validUUID(workspaceID) {
		return IssuedRecovery{}, ErrInvalidRecovery
	}
	locatorBytes, err := randomBytes(random, 16)
	if err != nil {
		return IssuedRecovery{}, err
	}
	secret, err := randomBytes(random, 32)
	if err != nil {
		return IssuedRecovery{}, err
	}
	salt, err := randomBytes(random, 16)
	if err != nil {
		return IssuedRecovery{}, err
	}
	locator := base64.RawURLEncoding.EncodeToString(locatorBytes)
	code := "mcr1." + workspaceID + "." + locator + "." + base64.RawURLEncoding.EncodeToString(secret)
	hash, err := recoveryKeyWithPermit(ctx, permit, secret, salt, recoveryTime, recoveryMemoryKiB, recoveryThreads, recoveryHashLength)
	if err != nil {
		return IssuedRecovery{}, err
	}
	verifier := RecoveryVerifier{
		Salt:      salt,
		Hash:      hash,
		Version:   argon2.Version,
		Time:      recoveryTime,
		MemoryKiB: recoveryMemoryKiB,
		Threads:   recoveryThreads,
	}
	return IssuedRecovery{Code: code, Locator: locator, Verifier: verifier}, nil
}

func RecoveryLocator(code string) (string, string, error) {
	parsed, err := parseRecoveryCode(code)
	if err != nil {
		return "", "", ErrInvalidRecovery
	}
	return parsed.workspaceID, parsed.locator, nil
}

func VerifyRecovery(ctx context.Context, code, workspaceID, locator string, verifier RecoveryVerifier) error {
	permit, err := AcquireRecoveryPermit(ctx)
	if err != nil {
		return err
	}
	defer permit.Release()
	return VerifyRecoveryWithPermit(ctx, permit, code, workspaceID, locator, verifier)
}

func VerifyRecoveryWithPermit(ctx context.Context, permit *RecoveryPermit, code, workspaceID, locator string, verifier RecoveryVerifier) error {
	if permit == nil || permit.state == nil || permit.state.limiter != processArgon2Limiter {
		return errInvalidRecoveryPermit
	}
	parsed, err := parseRecoveryCode(code)
	if err != nil || parsed.workspaceID != workspaceID || parsed.locator != locator {
		return ErrInvalidRecovery
	}
	if verifier.Version != argon2.Version || verifier.Time != recoveryTime || verifier.MemoryKiB != recoveryMemoryKiB || verifier.Threads != recoveryThreads || len(verifier.Salt) != 16 || len(verifier.Hash) != 32 {
		return ErrInvalidRecovery
	}
	actual, err := recoveryKeyWithPermit(ctx, permit, parsed.secret, verifier.Salt, verifier.Time, verifier.MemoryKiB, verifier.Threads, uint32(len(verifier.Hash)))
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(actual, verifier.Hash) != 1 {
		return ErrInvalidRecovery
	}
	return nil
}

type parsedRecoveryCode struct {
	workspaceID string
	locator     string
	secret      []byte
}

func parseRecoveryCode(code string) (parsedRecoveryCode, error) {
	parts, ok := cutCanonicalSecretFormat(code)
	if !ok || parts[0] != "mcr1" || !validUUID(parts[1]) {
		return parsedRecoveryCode{}, ErrInvalidRecovery
	}
	if _, err := decodeCanonicalRawURL(parts[2], 16); err != nil {
		return parsedRecoveryCode{}, ErrInvalidRecovery
	}
	secret, err := decodeCanonicalRawURL(parts[3], 32)
	if err != nil {
		return parsedRecoveryCode{}, ErrInvalidRecovery
	}
	return parsedRecoveryCode{workspaceID: parts[1], locator: parts[2], secret: secret}, nil
}
