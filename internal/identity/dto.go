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

type ApprovalResponse struct {
	PairingID      string    `json:"pairing_id"`
	Status         string    `json:"status"`
	ClaimExpiresAt time.Time `json:"claim_expires_at"`
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
