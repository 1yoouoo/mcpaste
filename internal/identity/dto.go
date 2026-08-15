package identity

import "time"

type CredentialResponse struct {
	Kind  string `json:"kind"`
	Token string `json:"token"`
}

type GrantDevice struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Platform    string `json:"platform"`
	Role        string `json:"role"`
}

type WorkspaceGrant struct {
	WorkspaceID  string               `json:"workspace_id"`
	Device       GrantDevice          `json:"device"`
	Credentials  []CredentialResponse `json:"credentials"`
	RecoveryCode string               `json:"recovery_code,omitempty"`
}

type DeviceSummary struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	Platform    string    `json:"platform"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
	IsCurrent   bool      `json:"is_current"`
}

type PairingCreateResponse struct {
	PairingID   string    `json:"pairing_id"`
	QRPayload   string    `json:"qr_payload"`
	ShortCode   string    `json:"short_code"`
	ClaimSecret string    `json:"claim_secret"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type PairingDetails struct {
	PairingID      string     `json:"pairing_id"`
	ProposedName   string     `json:"proposed_name"`
	Platform       string     `json:"platform"`
	RequestedScope string     `json:"requested_scope"`
	Status         string     `json:"status"`
	ExpiresAt      time.Time  `json:"expires_at"`
	ClaimExpiresAt *time.Time `json:"claim_expires_at,omitempty"`
}

type PairingStatusResponse struct {
	PairingID      string     `json:"pairing_id"`
	Status         string     `json:"status"`
	ExpiresAt      time.Time  `json:"expires_at"`
	ClaimExpiresAt *time.Time `json:"claim_expires_at,omitempty"`
}

type ApprovalResponse struct {
	PairingID      string    `json:"pairing_id"`
	Status         string    `json:"status"`
	ClaimExpiresAt time.Time `json:"claim_expires_at"`
}

type PasteResponse struct {
	PasteID              string               `json:"paste_id"`
	RevisionID           string               `json:"revision_id"`
	AttachmentRevisionID string               `json:"attachment_revision_id,omitempty"`
	Kind                 string               `json:"kind"`
	ServerSequence       int64                `json:"server_sequence"`
	CreatedAt            time.Time            `json:"created_at"`
	ExpiresAt            time.Time            `json:"expires_at"`
	Deleted              bool                 `json:"deleted"`
	Text                 *string              `json:"text,omitempty"`
	Assets               []ImageAssetResponse `json:"assets,omitempty"`
}

type ImageAssetResponse struct {
	AssetIndex int       `json:"asset_index"`
	MIMEType   string    `json:"mime_type"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
	ByteSize   int64     `json:"byte_size"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type SyncEventResponse struct {
	Sequence       int64                 `json:"sequence"`
	EventType      string                `json:"event_type"`
	PasteID        string                `json:"paste_id"`
	RevisionID     string                `json:"revision_id"`
	Kind           string                `json:"kind"`
	ServerSequence int64                 `json:"server_sequence"`
	CreatedAt      time.Time             `json:"created_at"`
	ExpiresAt      time.Time             `json:"expires_at"`
	Deleted        bool                  `json:"deleted"`
	Text           *string               `json:"text,omitempty"`
	Assets         *[]ImageAssetResponse `json:"assets,omitempty"`
}

type SyncResponse struct {
	Cursor  int64               `json:"cursor"`
	HasMore bool                `json:"has_more"`
	Events  []SyncEventResponse `json:"events"`
}

type SnapshotResponse struct {
	Cursor int64           `json:"cursor"`
	Pastes []PasteResponse `json:"pastes"`
}

func grantDevice(device Device) GrantDevice {
	return GrantDevice{
		ID: device.ID, DisplayName: device.DisplayName, Platform: device.Platform, Role: device.Role,
	}
}

func deviceSummary(device Device) DeviceSummary {
	return DeviceSummary{
		ID: device.ID, DisplayName: device.DisplayName, Platform: device.Platform, Role: device.Role,
		CreatedAt: wireTime(device.CreatedAt), IsCurrent: device.IsCurrent,
	}
}

func wireTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Second)
}
