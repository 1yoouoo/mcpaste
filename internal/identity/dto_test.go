package identity

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestWorkspaceGrantJSONHasExactDeviceFields(t *testing.T) {
	value := WorkspaceGrant{
		WorkspaceID: "00000000-0000-4000-8000-000000000101",
		Device: GrantDevice{
			ID:          "00000000-0000-4000-8000-000000000201",
			DisplayName: "MacBook Pro",
			Platform:    "macos",
			Role:        "full",
		},
		Credentials: []CredentialResponse{},
	}
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := []byte(`{"workspace_id":"00000000-0000-4000-8000-000000000101","device":{"id":"00000000-0000-4000-8000-000000000201","display_name":"MacBook Pro","platform":"macos","role":"full"},"credentials":[]}`)
	if !bytes.Equal(got, want) {
		t.Fatal("WorkspaceGrant JSON field set differs")
	}
}

func TestDeviceSummaryJSONAlwaysIncludesCurrentAndUTCSeconds(t *testing.T) {
	value := deviceSummary(Device{
		ID:          "00000000-0000-4000-8000-000000000201",
		DisplayName: "MacBook Pro",
		Platform:    "macos",
		Role:        "full",
		CreatedAt:   time.Date(2026, 8, 12, 21, 0, 0, 987654321, time.FixedZone("KST", 9*60*60)),
		IsCurrent:   false,
	})
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := []byte(`{"id":"00000000-0000-4000-8000-000000000201","display_name":"MacBook Pro","platform":"macos","role":"full","created_at":"2026-08-12T12:00:00Z","is_current":false}`)
	if !bytes.Equal(got, want) {
		t.Fatal("DeviceSummary JSON field set or timestamp precision differs")
	}
}

func TestPairingResponseTimesUseUTCSeconds(t *testing.T) {
	const pairingIDForDTOTest = "00000000-0000-4000-8000-000000000301"
	value := PairingCreateResponse{
		PairingID:   pairingIDForDTOTest,
		QRPayload:   "mcpaste-pairing:00000000-0000-4000-8000-000000000301",
		ShortCode:   "23456789",
		ClaimSecret: "test-value-not-a-credential",
		ExpiresAt:   wireTime(time.Date(2026, 8, 12, 12, 5, 0, 999, time.UTC)),
	}
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := []byte(`{"pairing_id":"00000000-0000-4000-8000-000000000301","qr_payload":"mcpaste-pairing:00000000-0000-4000-8000-000000000301","short_code":"23456789","claim_secret":"test-value-not-a-credential","expires_at":"2026-08-12T12:05:00Z"}`)
	if !bytes.Equal(got, want) {
		t.Fatal("PairingCreateResponse JSON field set or timestamp precision differs")
	}
	approval, err := json.Marshal(ApprovalResponse{
		PairingID:      pairingIDForDTOTest,
		Status:         "approved",
		ClaimExpiresAt: wireTime(time.Date(2026, 8, 12, 12, 10, 0, 999, time.UTC)),
	})
	if err != nil {
		t.Fatalf("Marshal() approval error = %v", err)
	}
	wantApproval := []byte(`{"pairing_id":"00000000-0000-4000-8000-000000000301","status":"approved","claim_expires_at":"2026-08-12T12:10:00Z"}`)
	if !bytes.Equal(approval, wantApproval) {
		t.Fatal("ApprovalResponse JSON field set or timestamp precision differs")
	}
	claimExpiry := wireTime(time.Date(2026, 8, 12, 12, 10, 0, 999, time.UTC))
	details, err := json.Marshal(PairingDetails{
		PairingID: pairingIDForDTOTest, ProposedName: "Build Host", Platform: "linux",
		RequestedScope: "connector", Status: "approved",
		ExpiresAt:      wireTime(time.Date(2026, 8, 12, 12, 5, 0, 999, time.UTC)),
		ClaimExpiresAt: &claimExpiry,
	})
	if err != nil {
		t.Fatalf("Marshal() details error = %v", err)
	}
	wantDetails := []byte(`{"pairing_id":"00000000-0000-4000-8000-000000000301","proposed_name":"Build Host","platform":"linux","requested_scope":"connector","status":"approved","expires_at":"2026-08-12T12:05:00Z","claim_expires_at":"2026-08-12T12:10:00Z"}`)
	if !bytes.Equal(details, wantDetails) {
		t.Fatal("PairingDetails JSON field set or timestamp precision differs")
	}
}
