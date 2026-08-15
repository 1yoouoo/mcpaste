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

func TestPasteResponseJSONIncludesAttachmentRevisionID(t *testing.T) {
	text := "hello"
	value := PasteResponse{
		PasteID: "paste-1", RevisionID: "revision-1", AttachmentRevisionID: "revision-2",
		Kind: RevisionContent, ServerSequence: 7,
		CreatedAt: wireTime(time.Date(2026, 8, 14, 12, 0, 0, 987654321, time.FixedZone("KST", 9*60*60))),
		ExpiresAt: wireTime(time.Date(2026, 8, 14, 13, 0, 0, 987654321, time.FixedZone("KST", 9*60*60))),
		Text:      &text,
	}
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := []byte(`{"paste_id":"paste-1","revision_id":"revision-1","attachment_revision_id":"revision-2","kind":"content","server_sequence":7,"created_at":"2026-08-14T03:00:00Z","expires_at":"2026-08-14T04:00:00Z","deleted":false,"text":"hello"}`)
	if !bytes.Equal(got, want) {
		t.Fatalf("PasteResponse JSON = %s, want %s", got, want)
	}
}

func TestContentSyncEventJSONOmitsUntouchedAssets(t *testing.T) {
	text := "hello"
	value := SyncEventResponse{
		Sequence: 8, EventType: "updated", PasteID: "paste-1", RevisionID: "revision-1",
		Kind: RevisionContent, ServerSequence: 7,
		CreatedAt: wireTime(time.Date(2026, 8, 14, 3, 0, 0, 999, time.UTC)),
		ExpiresAt: wireTime(time.Date(2026, 8, 14, 4, 0, 0, 999, time.UTC)),
		Text:      &text,
		Assets:    (*[]ImageAssetResponse)(nil),
	}
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := []byte(`{"sequence":8,"event_type":"updated","paste_id":"paste-1","revision_id":"revision-1","kind":"content","server_sequence":7,"created_at":"2026-08-14T03:00:00Z","expires_at":"2026-08-14T04:00:00Z","deleted":false,"text":"hello"}`)
	if !bytes.Equal(got, want) {
		t.Fatalf("content SyncEventResponse JSON = %s, want %s", got, want)
	}
}

func TestAttachmentSyncEventJSONEncodesExplicitClear(t *testing.T) {
	assets := []ImageAssetResponse{}
	value := SyncEventResponse{
		Sequence: 9, EventType: "updated", PasteID: "paste-1", RevisionID: "revision-2",
		Kind: RevisionAttachmentBundle, ServerSequence: 8,
		CreatedAt: wireTime(time.Date(2026, 8, 14, 3, 5, 0, 999, time.UTC)),
		ExpiresAt: wireTime(time.Date(2026, 8, 15, 3, 5, 0, 999, time.UTC)),
		Assets:    &assets,
	}
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := []byte(`{"sequence":9,"event_type":"updated","paste_id":"paste-1","revision_id":"revision-2","kind":"attachment_bundle","server_sequence":8,"created_at":"2026-08-14T03:05:00Z","expires_at":"2026-08-15T03:05:00Z","deleted":false,"assets":[]}`)
	if !bytes.Equal(got, want) {
		t.Fatalf("attachment SyncEventResponse JSON = %s, want %s", got, want)
	}
}

func TestImageAssetResponsesJSONUsesUTCSeconds(t *testing.T) {
	assets := imageAssetResponses([]ImageAsset{{
		AssetIndex: 0, MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 68,
		ExpiresAt: time.Date(2026, 8, 15, 12, 5, 0, 987654321, time.FixedZone("KST", 9*60*60)),
	}})
	got, err := json.Marshal(assets)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := []byte(`[{"asset_index":0,"mime_type":"image/png","width":1,"height":1,"byte_size":68,"expires_at":"2026-08-15T03:05:00Z"}]`)
	if !bytes.Equal(got, want) {
		t.Fatalf("imageAssetResponses JSON = %s, want %s", got, want)
	}
}

func TestImageResponseJSONUsesUTCSecondsForAssetExpiry(t *testing.T) {
	value := imageResponse(TextRevision{
		PasteID: "paste-1", RevisionID: "revision-2", ServerSequence: 8,
		CreatedAt: time.Date(2026, 8, 14, 12, 5, 0, 987654321, time.FixedZone("KST", 9*60*60)),
		ExpiresAt: time.Date(2026, 8, 15, 12, 5, 0, 987654321, time.FixedZone("KST", 9*60*60)),
		Assets: []ImageAsset{{
			AssetIndex: 0, MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 68,
			ExpiresAt: time.Date(2026, 8, 15, 12, 5, 0, 987654321, time.FixedZone("KST", 9*60*60)),
		}},
	})
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := []byte(`{"paste_id":"paste-1","revision_id":"revision-2","kind":"image_bundle","server_sequence":8,"created_at":"2026-08-14T03:05:00Z","expires_at":"2026-08-15T03:05:00Z","deleted":false,"assets":[{"asset_index":0,"mime_type":"image/png","width":1,"height":1,"byte_size":68,"expires_at":"2026-08-15T03:05:00Z"}]}`)
	if !bytes.Equal(got, want) {
		t.Fatalf("imageResponse JSON = %s, want %s", got, want)
	}
}

func TestRevisionNamesAreStable(t *testing.T) {
	cases := []struct {
		got  string
		want string
	}{
		{got: RevisionContent, want: "content"},
		{got: RevisionTombstone, want: "tombstone"},
		{got: RevisionImageBundle, want: "image_bundle"},
		{got: RevisionAttachmentBundle, want: "attachment_bundle"},
	}
	for _, test := range cases {
		if test.got != test.want {
			t.Errorf("revision name = %q, want %q", test.got, test.want)
		}
	}
}
