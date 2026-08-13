package identity

import (
	"context"
	"time"
)

type Store interface {
	WithinTx(context.Context, func(TxStore) error) error
	Authenticate(context.Context, string, string, []byte, time.Time) (Principal, error)
	ConsumeRateLimit(context.Context, RateRule, []byte, time.Time) (RateDecision, error)
}

type TxStore interface {
	GetIdempotency(context.Context, string, string, []byte) (IdempotencyRecord, error)
	DeleteIdempotency(context.Context, string, string, []byte) error
	PutIdempotency(context.Context, IdempotencyRecord) error
	InsertWorkspace(context.Context, string, time.Time) error
	InsertDevice(context.Context, string, Device) (Device, error)
	InsertCredential(context.Context, string, CredentialRecord) error
	GetRecovery(context.Context, string, string) (RecoveryRecord, error)
	PutRecovery(context.Context, string, RecoveryRecord) error
}
