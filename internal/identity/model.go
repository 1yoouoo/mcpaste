package identity

import (
	"errors"
	"time"

	"github.com/1yoouoo/mcpaste/internal/secure"
)

const MaxJSONBodyBytes int64 = 4096
const PairingLifetime = 5 * time.Minute
const ClaimLifetime = 5 * time.Minute
const IdempotencyLifetime = 24 * time.Hour
const EventLifetime = 35 * 24 * time.Hour
const PairingMetadataLifetime = 24 * time.Hour
const RateLimitRetention = 24 * time.Hour

var ErrInvalid = errors.New("invalid identity input")
var ErrUnauthorized = errors.New("unauthorized")
var ErrForbidden = errors.New("forbidden")
var ErrNotFound = errors.New("not found")
var ErrIdempotencyConflict = errors.New("idempotency conflict")
var ErrPairingPending = errors.New("pairing pending")
var ErrPairingApproved = errors.New("pairing already approved")
var ErrPairingExpired = errors.New("pairing expired")
var ErrInvalidClaim = errors.New("invalid claim")
var ErrInvalidRecovery = errors.New("invalid recovery")
var ErrRateLimited = errors.New("rate limited")

type Clock interface {
	Now() time.Time
}

type Result struct {
	Status int
	Body   []byte
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type Device struct {
	ID          string
	DisplayName string
	Platform    string
	Role        string
	CreatedAt   time.Time
	IsCurrent   bool
}

type Principal struct {
	WorkspaceID string
	DeviceID    string
	Scope       string
}

type CredentialRecord struct {
	DeviceID  string
	Locator   string
	Scope     string
	Hash      []byte
	CreatedAt time.Time
}

type RecoveryRecord struct {
	WorkspaceID string
	Locator     string
	Verifier    secure.RecoveryVerifier
	CreatedAt   time.Time
	RotatedAt   time.Time
}

type Pairing struct {
	ID                 string
	ShortCode          string
	ClaimHash          []byte
	ProposedName       string
	Platform           string
	RequestedScope     string
	WorkspaceID        string
	ApprovedByDeviceID string
	DeviceID           string
	CreatedAt          time.Time
	ExpiresAt          time.Time
	ApprovedAt         time.Time
	ClaimExpiresAt     time.Time
	ClaimedAt          time.Time
	ClaimInvalidatedAt time.Time
	Grant              secure.Envelope
	MetadataPurgeAt    time.Time
}

type StoredResponse struct {
	Status      int
	ContentType string
	Envelope    secure.Envelope
}

type IdempotencyRecord struct {
	ScopeID     string
	Operation   string
	KeyHash     []byte
	WorkspaceID string
	RequestHash []byte
	Response    StoredResponse
	CreatedAt   time.Time
	ExpiresAt   time.Time
	Expired     bool
}

type RateDecision struct {
	Allowed    bool
	RetryAfter time.Duration
}

type RateRule struct {
	Scope  string
	Limit  int
	Window time.Duration
}

type CleanupResult struct {
	RevokedDevices  int64
	PairingRows     int64
	IdempotencyRows int64
	EventRows       int64
	RateLimitRows   int64
}
