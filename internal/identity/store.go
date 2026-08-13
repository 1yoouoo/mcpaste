package identity

import (
	"context"
	"time"

	"github.com/1yoouoo/mcpaste/internal/images"
	"github.com/1yoouoo/mcpaste/internal/secure"
)

type Store interface {
	WithinTx(context.Context, func(TxStore) error) error
	Authenticate(context.Context, string, string, []byte, time.Time) (Principal, error)
	ConsumeRateLimit(context.Context, RateRule, []byte, time.Time) (RateDecision, error)
	Cleanup(context.Context, time.Time) (CleanupResult, error)
	PurgeText(context.Context, time.Time) (int64, int64, error)
	PurgeImages(context.Context, time.Time) (int64, int64, error)
	ListExpiredImages(context.Context, time.Time) ([]ImageAsset, error)
}

type TxStore interface {
	LockIdempotency(context.Context, string, string, []byte) error
	GetIdempotency(context.Context, string, string, []byte) (IdempotencyRecord, error)
	DeleteIdempotency(context.Context, string, string, []byte) error
	PutIdempotency(context.Context, IdempotencyRecord) error
	InsertWorkspace(context.Context, string, time.Time) error
	InsertDevice(context.Context, string, Device) (Device, error)
	InsertCredential(context.Context, string, CredentialRecord) error
	GetRecovery(context.Context, string, string) (RecoveryRecord, error)
	PutRecovery(context.Context, string, RecoveryRecord) error
	InsertPairing(context.Context, Pairing) error
	GetPairingByID(context.Context, string, string, time.Time) (Pairing, error)
	GetPairingByShortCode(context.Context, string, string, time.Time) (Pairing, error)
	LockPairingForApproval(context.Context, string, string, time.Time) (Pairing, error)
	ApprovePairing(context.Context, string, string, string, string, time.Time, time.Time, secure.Envelope, time.Time) error
	LockPairingForClaim(context.Context, string, []byte, time.Time) (Pairing, error)
	MarkPairingClaimed(context.Context, string, time.Time) error
	ListDevices(context.Context, string, string) ([]Device, error)
	RenameDevice(context.Context, string, string, string, time.Time) (Device, error)
	RevokeDevice(context.Context, string, string, time.Time) error
	InsertEvent(context.Context, string, string, string, time.Time) error
	InsertPaste(context.Context, string, string, time.Time) error
	SetPasteKind(context.Context, string, string, string) error
	AppendTextRevision(context.Context, string, string, string, string, string, secure.Envelope, time.Time, time.Time) (TextRevision, error)
	AppendImageRevision(context.Context, string, string, string, string, []ImageAsset, time.Time, time.Time) (TextRevision, error)
	ListImageAssets(context.Context, string, string, string) ([]ImageAsset, error)
	ListPastes(context.Context, string, time.Time, time.Time) ([]TextRevision, error)
	Snapshot(context.Context, string, time.Time) (SnapshotResult, error)
	Sync(context.Context, string, int64, int, time.Time) (SyncResult, error)
	LatestPaste(context.Context, string, time.Time) (LatestPaste, error)
	TouchPaste(context.Context, string, string, time.Time) error
}

type ImageStore interface {
	Put(string, string, string, int, []byte) (images.StoredAsset, error)
	Open(images.StoredAsset) ([]byte, error)
	Remove(images.StoredAsset) error
	RemoveTree(string, string, string) error
	RemovePaste(string, string) error
}
