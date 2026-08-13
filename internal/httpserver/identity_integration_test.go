package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/internal/database"
	"github.com/1yoouoo/mcpaste/internal/identity"
	identitypostgres "github.com/1yoouoo/mcpaste/internal/identity/postgres"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/1yoouoo/mcpaste/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

type mutableClock struct {
	mu    sync.Mutex
	value time.Time
}

func (c *mutableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

func (c *mutableClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.value = c.value.Add(duration)
	c.mu.Unlock()
}

type deterministicReader struct {
	mu       sync.Mutex
	counter  uint64
	buffered []byte
}

func (r *deterministicReader) Read(target []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	written := 0
	for written < len(target) {
		if len(r.buffered) == 0 {
			var encodedCounter [8]byte
			binary.BigEndian.PutUint64(encodedCounter[:], r.counter)
			hasher := sha256.New()
			_, _ = hasher.Write([]byte("mcpaste-integration-test-random-v1\x00"))
			_, _ = hasher.Write(encodedCounter[:])
			r.buffered = hasher.Sum(nil)
			r.counter++
		}
		copied := copy(target[written:], r.buffered)
		written += copied
		r.buffered = r.buffered[copied:]
	}
	return written, nil
}

func TestDeterministicReaderDoesNotRepeatBlocks(t *testing.T) {
	reader := &deterministicReader{}
	output := make([]byte, 4096)
	read, err := reader.Read(output)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if read != len(output) {
		t.Fatalf("Read() bytes = %d", read)
	}
	seen := make(map[[sha256.Size]byte]struct{}, len(output)/sha256.Size)
	for offset := 0; offset < len(output); offset += sha256.Size {
		var block [sha256.Size]byte
		copy(block[:], output[offset:offset+sha256.Size])
		if _, exists := seen[block]; exists {
			t.Fatalf("deterministic stream repeated block index %d", offset/sha256.Size)
		}
		seen[block] = struct{}{}
	}
}

type integrationHarness struct {
	t       *testing.T
	pool    *pgxpool.Pool
	clock   *mutableClock
	handler http.Handler
	logs    *bytes.Buffer
	key     int
}

func newIntegrationHarness(t *testing.T) *integrationHarness {
	t.Helper()
	pool := testdb.New(t)
	random := &deterministicReader{}
	keyring, err := secure.NewKeyring("test-key", map[string][]byte{"test-key": bytes.Repeat([]byte{0x31}, 32)}, random)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	clock := &mutableClock{value: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
	service := identity.NewService(identitypostgres.New(pool), keyring, random, clock)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	application := NewApplicationHandler(func(ctx context.Context) error { return database.Ready(ctx, pool) }, service, nil)
	handler := NewRecoveryMiddleware(logger)(NewAccessLogMiddleware(logger)(application))
	return &integrationHarness{t: t, pool: pool, clock: clock, handler: handler, logs: &logs}
}

func (h *integrationHarness) nextKey() string {
	h.key++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", h.key)
}

func (h *integrationHarness) request(method, path, bearer, idempotencyKey string, input any) (int, http.Header, []byte) {
	h.t.Helper()
	var body []byte
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			h.t.Fatalf("marshal request: %v", err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.RemoteAddr = "192.0.2.20:4000"
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response := httptest.NewRecorder()
	h.handler.ServeHTTP(response, request)
	return response.Code, response.Header(), response.Body.Bytes()
}

func (h *integrationHarness) createWorkspace(name string) (identity.WorkspaceGrant, string, []byte) {
	h.t.Helper()
	key := h.nextKey()
	status, _, body := h.request(http.MethodPost, "/v1/workspaces", "", key, map[string]any{"device_name": name, "platform": "macos"})
	if status != http.StatusCreated {
		h.t.Fatalf("workspace create status = %d", status)
	}
	var grant identity.WorkspaceGrant
	if err := json.Unmarshal(body, &grant); err != nil {
		h.t.Fatalf("decode workspace grant: %v", err)
	}
	return grant, key, bytes.Clone(body)
}

func credential(grant identity.WorkspaceGrant, kind string) string {
	for _, item := range grant.Credentials {
		if item.Kind == kind {
			return item.Token
		}
	}
	return ""
}

func TestIdentityLifecycleIntegration(t *testing.T) {
	h := newIntegrationHarness(t)
	workspace, workspaceKey, workspaceBody := h.createWorkspace("MacBook Pro")
	if len(workspace.Credentials) != 2 || workspace.Credentials[0].Kind != "full" || workspace.Credentials[1].Kind != "connector" {
		t.Fatalf("initial credential kinds are incorrect")
	}
	status, _, replayBody := h.request(http.MethodPost, "/v1/workspaces", "", workspaceKey, map[string]any{"device_name": "MacBook Pro", "platform": "macos"})
	if status != http.StatusCreated || !bytes.Equal(workspaceBody, replayBody) {
		t.Fatal("workspace idempotent replay differs")
	}
	fullToken := credential(workspace, "full")
	connectorToken := credential(workspace, "connector")
	if fullToken == "" || connectorToken == "" || fullToken == connectorToken {
		t.Fatal("separate initial credentials were not returned")
	}

	pairKey := h.nextKey()
	status, _, pairBody := h.request(http.MethodPost, "/v1/pairing-requests", "", pairKey, map[string]any{
		"proposed_name": "macbook pro", "platform": "linux", "requested_scope": "connector",
	})
	if status != http.StatusCreated {
		t.Fatalf("connector pairing create status = %d", status)
	}
	var pairing identity.PairingCreateResponse
	if err := json.Unmarshal(pairBody, &pairing); err != nil {
		t.Fatalf("decode pairing: %v", err)
	}
	if pairing.QRPayload != "mcpaste://pair/"+pairing.PairingID || strings.Contains(pairing.QRPayload, pairing.ClaimSecret) {
		t.Fatal("QR payload contains data beyond the pending identifier")
	}
	status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/claim", "", "", map[string]any{"claim_secret": pairing.ClaimSecret})
	if status != http.StatusConflict {
		t.Fatalf("pending claim status = %d", status)
	}
	status, _, detailsByID := h.request(http.MethodGet, "/v1/pairing-requests/"+pairing.PairingID, fullToken, "", nil)
	if status != http.StatusOK || bytes.Contains(detailsByID, []byte(pairing.ClaimSecret)) || !bytes.Contains(detailsByID, []byte(`"status":"pending"`)) {
		t.Fatalf("pairing detail status/leak = %d", status)
	}
	status, malformedHeaders, malformedBody := h.request(http.MethodGet, "/v1/pairing-requests/not-a-uuid", fullToken, "", nil)
	if status != http.StatusBadRequest || malformedHeaders.Get("Content-Type") != "application/json" || string(malformedBody) != "{\"error\":{\"code\":\"invalid_request\"}}\n" {
		t.Fatalf("malformed pairing UUID response metadata = %d/%q/%d", status, malformedHeaders.Get("Content-Type"), len(malformedBody))
	}
	status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/lookup", fullToken, "", map[string]any{"short_code": "I2345678"})
	if status != http.StatusBadRequest {
		t.Fatalf("malformed short-code status = %d", status)
	}
	var malformedLookupRateRows int
	if err := h.pool.QueryRow(context.Background(), `
select count(*) from rate_limit_buckets where scope = 'pairing.lookup'`).Scan(&malformedLookupRateRows); err != nil {
		t.Fatalf("count malformed lookup rate rows: %v", err)
	}
	if malformedLookupRateRows != 0 {
		t.Fatalf("malformed lookup rate rows = %d", malformedLookupRateRows)
	}
	status, _, detailsBody := h.request(http.MethodPost, "/v1/pairing-requests/lookup", fullToken, "", map[string]any{"short_code": pairing.ShortCode})
	if status != http.StatusOK || bytes.Contains(detailsBody, []byte(pairing.ClaimSecret)) {
		t.Fatalf("pairing lookup status/leak = %d", status)
	}
	status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/approve", connectorToken, h.nextKey(), map[string]any{})
	if status != http.StatusForbidden {
		t.Fatalf("connector approval status = %d", status)
	}
	status, _, _ = h.request(http.MethodPatch, "/v1/devices/"+workspace.Device.ID, connectorToken, h.nextKey(), map[string]any{"display_name": "Forbidden Rename"})
	if status != http.StatusForbidden {
		t.Fatalf("connector rename status = %d", status)
	}
	approvalKey := h.nextKey()
	status, _, approvalBody := h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/approve", fullToken, approvalKey, map[string]any{})
	if status != http.StatusOK || bytes.Contains(approvalBody, []byte(pairing.ClaimSecret)) || bytes.Contains(approvalBody, []byte("mcp1.")) {
		t.Fatalf("approval status or response leak = %d", status)
	}
	status, _, approvalReplay := h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/approve", fullToken, approvalKey, map[string]any{})
	if status != http.StatusOK || !bytes.Equal(approvalBody, approvalReplay) {
		t.Fatal("approval idempotent replay was not byte-identical")
	}
	var devicesAfterApproval int
	if err := h.pool.QueryRow(context.Background(), `
select count(*) from devices where workspace_id = $1::uuid`, workspace.WorkspaceID).Scan(&devicesAfterApproval); err != nil {
		t.Fatalf("count devices after approval replay: %v", err)
	}
	if devicesAfterApproval != 2 {
		t.Fatalf("devices after approval replay = %d", devicesAfterApproval)
	}
	status, _, firstClaim := h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/claim", "", "", map[string]any{"claim_secret": pairing.ClaimSecret})
	if status != http.StatusOK {
		t.Fatalf("first claim status = %d", status)
	}
	status, _, secondClaim := h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/claim", "", "", map[string]any{"claim_secret": pairing.ClaimSecret})
	if status != http.StatusOK || !bytes.Equal(firstClaim, secondClaim) {
		t.Fatal("claim replay changed credentials")
	}
	var connectorGrant identity.WorkspaceGrant
	if err := json.Unmarshal(firstClaim, &connectorGrant); err != nil {
		t.Fatalf("decode connector grant: %v", err)
	}
	if len(connectorGrant.Credentials) != 1 || connectorGrant.Credentials[0].Kind != "connector" || connectorGrant.Device.DisplayName != "macbook pro (2)" {
		t.Fatal("connector grant count, scope, or suffix is incorrect")
	}
	status, _, _ = h.request(http.MethodGet, "/v1/devices", connectorGrant.Credentials[0].Token, "", nil)
	if status != http.StatusForbidden {
		t.Fatalf("connector device administration status = %d", status)
	}

	fullPairKey := h.nextKey()
	status, _, fullPairBody := h.request(http.MethodPost, "/v1/pairing-requests", "", fullPairKey, map[string]any{
		"proposed_name": "MacBook Pro", "platform": "macos", "requested_scope": "full",
	})
	if status != http.StatusCreated {
		t.Fatalf("full pairing create status = %d", status)
	}
	var fullPair identity.PairingCreateResponse
	if err := json.Unmarshal(fullPairBody, &fullPair); err != nil {
		t.Fatalf("decode full pairing: %v", err)
	}
	status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/"+fullPair.PairingID+"/approve", fullToken, h.nextKey(), map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("full approval status = %d", status)
	}
	status, _, fullClaimBody := h.request(http.MethodPost, "/v1/pairing-requests/"+fullPair.PairingID+"/claim", "", "", map[string]any{"claim_secret": fullPair.ClaimSecret})
	if status != http.StatusOK {
		t.Fatalf("full claim status = %d", status)
	}
	var joinedFull identity.WorkspaceGrant
	if err := json.Unmarshal(fullClaimBody, &joinedFull); err != nil {
		t.Fatalf("decode full grant: %v", err)
	}
	if len(joinedFull.Credentials) != 2 || joinedFull.Credentials[0].Kind != "full" || joinedFull.Credentials[1].Kind != "connector" || joinedFull.Device.DisplayName != "MacBook Pro (3)" {
		t.Fatal("full pairing credential count, order, or suffix is incorrect")
	}
	joinedFullToken := credential(joinedFull, "full")
	if joinedFullToken == "" {
		t.Fatal("joined full credential is missing")
	}
	status, _, _ = h.request(http.MethodDelete, "/v1/devices/"+workspace.Device.ID, fullToken, h.nextKey(), nil)
	if status != http.StatusBadRequest {
		t.Fatalf("self-revocation status = %d", status)
	}
	var selfRevokeRows int
	if err := h.pool.QueryRow(context.Background(), `
select count(*) from idempotency_records where scope_id = $1 and operation = $2`, workspace.WorkspaceID, "device.revoke:"+workspace.Device.ID).Scan(&selfRevokeRows); err != nil {
		t.Fatalf("count self-revocation idempotency rows: %v", err)
	}
	if selfRevokeRows != 0 {
		t.Fatalf("self-revocation idempotency rows = %d", selfRevokeRows)
	}
	status, _, _ = h.request(http.MethodGet, "/v1/devices", fullToken, "", nil)
	if status != http.StatusOK {
		t.Fatalf("self-revocation mutated current device: %d", status)
	}

	status, _, devicesBody := h.request(http.MethodGet, "/v1/devices", fullToken, "", nil)
	if status != http.StatusOK {
		t.Fatalf("device list status = %d", status)
	}
	var list struct {
		Devices []identity.DeviceSummary `json:"devices"`
	}
	if err := json.Unmarshal(devicesBody, &list); err != nil || len(list.Devices) != 3 {
		t.Fatalf("device list count/decode = %d/%v", len(list.Devices), err)
	}
	status, _, renameBody := h.request(http.MethodPatch, "/v1/devices/"+connectorGrant.Device.ID, fullToken, h.nextKey(), map[string]any{"display_name": "MACBOOK PRO"})
	if status != http.StatusOK || !bytes.Contains(renameBody, []byte(`"display_name":"MACBOOK PRO (2)"`)) {
		t.Fatalf("duplicate rename status/result = %d", status)
	}
	revokeKey := h.nextKey()
	status, _, _ = h.request(http.MethodDelete, "/v1/devices/"+connectorGrant.Device.ID, connectorToken, h.nextKey(), nil)
	if status != http.StatusForbidden {
		t.Fatalf("connector revocation status = %d", status)
	}
	status, _, _ = h.request(http.MethodDelete, "/v1/devices/"+connectorGrant.Device.ID, fullToken, revokeKey, nil)
	if status != http.StatusNoContent {
		t.Fatalf("revoke status = %d", status)
	}
	status, _, _ = h.request(http.MethodDelete, "/v1/devices/"+connectorGrant.Device.ID, fullToken, revokeKey, nil)
	if status != http.StatusNoContent {
		t.Fatalf("revoke replay status = %d", status)
	}
	status, _, _ = h.request(http.MethodGet, "/v1/devices", connectorGrant.Credentials[0].Token, "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked auth status = %d", status)
	}

	other, _, _ := h.createWorkspace("Other Mac")
	otherFull := credential(other, "full")
	status, _, _ = h.request(http.MethodPatch, "/v1/devices/"+workspace.Device.ID, otherFull, h.nextKey(), map[string]any{"display_name": "Cross Workspace"})
	if status != http.StatusNotFound {
		t.Fatalf("cross-workspace rename status = %d", status)
	}
	status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/"+fullPair.PairingID+"/approve", otherFull, h.nextKey(), map[string]any{})
	if status != http.StatusNotFound {
		t.Fatalf("cross-workspace pairing status = %d", status)
	}

	recoveryKey := h.nextKey()
	status, _, recoveryBody := h.request(http.MethodPost, "/v1/recoveries", "", recoveryKey, map[string]any{
		"recovery_code": workspace.RecoveryCode, "device_name": "Recovered Mac", "platform": "macos",
	})
	if status != http.StatusCreated {
		t.Fatalf("recovery status = %d", status)
	}
	status, _, recoveryReplay := h.request(http.MethodPost, "/v1/recoveries", "", recoveryKey, map[string]any{
		"recovery_code": workspace.RecoveryCode, "device_name": "Recovered Mac", "platform": "macos",
	})
	if status != http.StatusCreated || !bytes.Equal(recoveryBody, recoveryReplay) {
		t.Fatal("recovery idempotent replay differs")
	}
	var recovered identity.WorkspaceGrant
	if err := json.Unmarshal(recoveryBody, &recovered); err != nil {
		t.Fatalf("decode recovery grant: %v", err)
	}
	if recovered.WorkspaceID != workspace.WorkspaceID || recovered.RecoveryCode == workspace.RecoveryCode || len(recovered.Credentials) != 2 {
		t.Fatal("recovery did not rotate or issue exact full credentials")
	}
	status, _, _ = h.request(http.MethodGet, "/v1/devices", fullToken, "", nil)
	if status != http.StatusOK {
		t.Fatalf("existing full device not preserved: %d", status)
	}
	status, _, _ = h.request(http.MethodPost, "/v1/recoveries", "", h.nextKey(), map[string]any{
		"recovery_code": workspace.RecoveryCode, "device_name": "Old Code", "platform": "macos",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("old recovery code status = %d", status)
	}
	if _, err := h.pool.Exec(context.Background(), `
update recovery_verifiers set verifier = decode(repeat('ff', 32), 'hex')
where workspace_id = $1::uuid`, workspace.WorkspaceID); err != nil {
		t.Fatalf("corrupt verifier: %v", err)
	}
	status, _, _ = h.request(http.MethodPost, "/v1/recoveries", "", h.nextKey(), map[string]any{
		"recovery_code": recovered.RecoveryCode, "device_name": "Corrupt Verifier", "platform": "macos",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("corrupt verifier recovery status = %d", status)
	}
	status, _, _ = h.request(http.MethodDelete, "/v1/devices/"+workspace.Device.ID, joinedFullToken, h.nextKey(), nil)
	if status != http.StatusNoContent {
		t.Fatalf("other-full-device revocation status = %d", status)
	}
	status, _, _ = h.request(http.MethodGet, "/v1/devices", fullToken, "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("other-full-device revocation auth status = %d", status)
	}

	var claimHashLength int
	if err := h.pool.QueryRow(context.Background(), `
select octet_length(claim_hash) from pairing_requests where id = $1::uuid`, pairing.PairingID).Scan(&claimHashLength); err != nil {
		t.Fatalf("inspect claim hash: %v", err)
	}
	if claimHashLength != 32 {
		t.Fatalf("claim hash length = %d", claimHashLength)
	}
	for _, marker := range []string{pairing.ClaimSecret, pairing.ShortCode, pairing.QRPayload, fullToken, connectorToken, workspace.RecoveryCode} {
		if strings.Contains(h.logs.String(), marker) {
			t.Fatal("access logs contain an identity secret marker")
		}
	}
}

func TestPasteHTTPContractAndConnectorIsolation(t *testing.T) {
	h := newIntegrationHarness(t)
	workspace, _, _ := h.createWorkspace("Paste Mac")
	fullToken := credential(workspace, "full")
	connectorToken := credential(workspace, "connector")
	exact := "  first\r\nsecond\n끝  "

	createKey := h.nextKey()
	status, _, body := h.request(http.MethodPost, "/v1/pastes", fullToken, createKey, map[string]any{"text": exact})
	if status != http.StatusCreated {
		t.Fatalf("create paste status = %d/%q", status, body)
	}
	var created identity.PasteResponse
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode create paste: %v", err)
	}
	if created.Text == nil || *created.Text != exact || created.Deleted || created.Kind != "content" {
		t.Fatalf("created paste = %#v", created)
	}
	status, _, replayBody := h.request(http.MethodPost, "/v1/pastes", fullToken, createKey, map[string]any{"text": exact})
	if status != http.StatusCreated || !bytes.Equal(body, replayBody) {
		t.Fatalf("create replay = %d/%q", status, replayBody)
	}

	updateKey := h.nextKey()
	status, _, body = h.request(http.MethodPatch, "/v1/pastes/"+created.PasteID, fullToken, updateKey, map[string]any{"text": "updated"})
	if status != http.StatusOK {
		t.Fatalf("update paste status = %d/%q", status, body)
	}
	status, _, body = h.request(http.MethodGet, "/v1/pastes", fullToken, "", nil)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"text":"updated"`)) || bytes.Contains(body, []byte(exact)) {
		t.Fatalf("paste history status/body = %d/%q", status, body)
	}

	status, _, body = h.request(http.MethodGet, "/v1/sync?after=0&limit=1", fullToken, "", nil)
	var firstSync identity.SyncResponse
	if err := json.Unmarshal(body, &firstSync); err != nil {
		t.Fatalf("decode sync: %v", err)
	}
	if status != http.StatusOK || !firstSync.HasMore || len(firstSync.Events) != 1 || firstSync.Events[0].Text == nil || *firstSync.Events[0].Text != exact {
		t.Fatalf("sync status/body = %d/%q", status, body)
	}
	status, _, body = h.request(http.MethodGet, "/v1/sync?after=not-a-sequence", fullToken, "", nil)
	if status != http.StatusBadRequest || string(body) != "{\"error\":{\"code\":\"invalid_request\"}}\n" {
		t.Fatalf("malformed sync status/body = %d/%q", status, body)
	}

	connectorPaths := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/v1/pastes", map[string]any{"text": "connector write"}},
		{http.MethodPatch, "/v1/pastes/" + created.PasteID, map[string]any{"text": "connector update"}},
		{http.MethodDelete, "/v1/pastes/" + created.PasteID, nil},
		{http.MethodGet, "/v1/pastes", nil},
		{http.MethodGet, "/v1/sync?after=0", nil},
	}
	for _, item := range connectorPaths {
		status, _, body = h.request(item.method, item.path, connectorToken, h.nextKey(), item.body)
		if status != http.StatusForbidden {
			t.Fatalf("connector %s %s status/body = %d/%q", item.method, item.path, status, body)
		}
	}

	deleteKey := h.nextKey()
	status, _, body = h.request(http.MethodDelete, "/v1/pastes/"+created.PasteID, fullToken, deleteKey, nil)
	if status != http.StatusNoContent || len(body) != 0 {
		t.Fatalf("delete status/body = %d/%q", status, body)
	}
	status, _, replayBody = h.request(http.MethodDelete, "/v1/pastes/"+created.PasteID, fullToken, deleteKey, nil)
	if status != http.StatusNoContent || len(replayBody) != 0 {
		t.Fatalf("delete replay status/body = %d/%q", status, replayBody)
	}
	status, _, body = h.request(http.MethodGet, "/v1/pastes", fullToken, "", nil)
	if status != http.StatusOK || string(body) != "{\"pastes\":[]}\n" {
		t.Fatalf("history after delete = %d/%q", status, body)
	}
}

func TestPairingExpiryIntegration(t *testing.T) {
	t.Run("pending request", func(t *testing.T) {
		h := newIntegrationHarness(t)
		workspace, _, _ := h.createWorkspace("Approver")
		status, _, body := h.request(http.MethodPost, "/v1/pairing-requests", "", h.nextKey(), map[string]any{
			"proposed_name": "Expiring", "platform": "linux", "requested_scope": "connector",
		})
		if status != http.StatusCreated {
			t.Fatalf("pairing create status = %d", status)
		}
		var pairing identity.PairingCreateResponse
		if err := json.Unmarshal(body, &pairing); err != nil {
			t.Fatalf("decode pairing: %v", err)
		}
		h.clock.Advance(identity.PairingLifetime + time.Second)
		fullToken := credential(workspace, "full")
		status, _, _ = h.request(http.MethodGet, "/v1/pairing-requests/"+pairing.PairingID, fullToken, "", nil)
		if status != http.StatusGone {
			t.Fatalf("expired details status = %d", status)
		}
		status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/approve", fullToken, h.nextKey(), map[string]any{})
		if status != http.StatusGone {
			t.Fatalf("expired approval status = %d", status)
		}
		status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/claim", "", "", map[string]any{"claim_secret": pairing.ClaimSecret})
		if status != http.StatusGone {
			t.Fatalf("expired claim status = %d", status)
		}
	})

	t.Run("approved details expire before private claim", func(t *testing.T) {
		h := newIntegrationHarness(t)
		workspace, _, _ := h.createWorkspace("Approver")
		fullToken := credential(workspace, "full")
		status, _, body := h.request(http.MethodPost, "/v1/pairing-requests", "", h.nextKey(), map[string]any{
			"proposed_name": "Approved Expiry", "platform": "linux", "requested_scope": "connector",
		})
		if status != http.StatusCreated {
			t.Fatalf("pairing create status = %d", status)
		}
		var pairing identity.PairingCreateResponse
		if err := json.Unmarshal(body, &pairing); err != nil {
			t.Fatalf("decode pairing: %v", err)
		}
		h.clock.Advance(4 * time.Minute)
		status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/approve", fullToken, h.nextKey(), map[string]any{})
		if status != http.StatusOK {
			t.Fatalf("approval status = %d", status)
		}
		h.clock.Advance(time.Minute + time.Second)
		status, _, _ = h.request(http.MethodGet, "/v1/pairing-requests/"+pairing.PairingID, fullToken, "", nil)
		if status != http.StatusGone {
			t.Fatalf("approved expired ID details status = %d", status)
		}
		status, _, _ = h.request(http.MethodPost, "/v1/pairing-requests/lookup", fullToken, "", map[string]any{"short_code": pairing.ShortCode})
		if status != http.StatusGone {
			t.Fatalf("approved expired short-code details status = %d", status)
		}
		status, _, claimBody := h.request(http.MethodPost, "/v1/pairing-requests/"+pairing.PairingID+"/claim", "", "", map[string]any{"claim_secret": pairing.ClaimSecret})
		if status != http.StatusOK || len(claimBody) == 0 {
			t.Fatalf("private claim status/body bytes = %d/%d", status, len(claimBody))
		}
	})
}

func rateLimitSubjectHash(scope, subject string) []byte {
	digest := sha256.Sum256([]byte("mcpaste-rate-v1\x00" + scope + "\x00" + subject))
	return digest[:]
}

func rateLimitCount(t *testing.T, h *integrationHarness, scope, subject string) int {
	t.Helper()
	var count int
	if err := h.pool.QueryRow(context.Background(), `
select request_count
from rate_limit_buckets
where scope = $1 and subject_hash = $2`, scope, rateLimitSubjectHash(scope, subject)).Scan(&count); err != nil {
		t.Fatalf("inspect rate-limit count for scope %q: %v", scope, err)
	}
	return count
}

func rateLimitTotals(t *testing.T, h *integrationHarness) (int, int) {
	t.Helper()
	var rows int
	var requests int
	if err := h.pool.QueryRow(context.Background(), `
select count(*), coalesce(sum(request_count), 0)
from rate_limit_buckets`).Scan(&rows, &requests); err != nil {
		t.Fatalf("inspect rate-limit totals: %v", err)
	}
	return rows, requests
}

func assertFixedRateLimit(
	t *testing.T,
	h *integrationHarness,
	scope string,
	subject string,
	limit int,
	window time.Duration,
	request func() (int, http.Header, []byte),
) {
	t.Helper()
	now := h.clock.Now()
	resetIn := 1250 * time.Millisecond
	windowStartedAt := now.Add(-window).Add(resetIn)
	subjectHash := rateLimitSubjectHash(scope, subject)
	if _, err := h.pool.Exec(context.Background(), `
insert into rate_limit_buckets(scope, subject_hash, window_started_at, request_count, expires_at)
values ($1, $2, $3, $4, $5)`,
		scope, subjectHash, windowStartedAt, limit, now.Add(identity.RateLimitRetention),
	); err != nil {
		t.Fatalf("seed fixed rate limit: %v", err)
	}
	status, headers, _ := request()
	if status != http.StatusTooManyRequests || headers.Get("Retry-After") != "2" {
		t.Fatalf("rate-limit status/Retry-After = %d/%q", status, headers.Get("Retry-After"))
	}
	var requestCount int
	var storedWindow time.Time
	if err := h.pool.QueryRow(context.Background(), `
select request_count, window_started_at
from rate_limit_buckets
where scope = $1 and subject_hash = $2`, scope, subjectHash).Scan(&requestCount, &storedWindow); err != nil {
		t.Fatalf("inspect fixed rate limit: %v", err)
	}
	if requestCount != limit+1 || !storedWindow.Equal(windowStartedAt) {
		t.Fatalf("rate-limit count/window metadata = %d/%v", requestCount, storedWindow.Equal(windowStartedAt))
	}
}

func corruptRecoveryVerifier(t *testing.T, h *integrationHarness, workspaceID string) {
	t.Helper()
	if _, err := h.pool.Exec(context.Background(), `
update recovery_verifiers
set verifier = decode(repeat('ff', 32), 'hex')
where workspace_id = $1::uuid`, workspaceID); err != nil {
		t.Fatalf("corrupt recovery verifier: %v", err)
	}
}

func TestFixedRateLimitPoliciesIntegration(t *testing.T) {
	t.Run("workspace create 5 per hour by IP", func(t *testing.T) {
		h := newIntegrationHarness(t)
		assertFixedRateLimit(t, h, "workspace.create", "ip:192.0.2.20", 5, time.Hour, func() (int, http.Header, []byte) {
			return h.request(http.MethodPost, "/v1/workspaces", "", h.nextKey(), map[string]any{"device_name": "Limited Mac", "platform": "macos"})
		})
	})

	t.Run("pairing create 10 per 10 minutes by IP", func(t *testing.T) {
		h := newIntegrationHarness(t)
		assertFixedRateLimit(t, h, "pairing.create", "ip:192.0.2.20", 10, 10*time.Minute, func() (int, http.Header, []byte) {
			return h.request(http.MethodPost, "/v1/pairing-requests", "", h.nextKey(), map[string]any{
				"proposed_name": "Limited Pair", "platform": "linux", "requested_scope": "connector",
			})
		})
	})

	t.Run("lookup 30 per 5 minutes by workspace device", func(t *testing.T) {
		h := newIntegrationHarness(t)
		workspace, _, _ := h.createWorkspace("Lookup Mac")
		subject := workspace.WorkspaceID + ":" + workspace.Device.ID
		assertFixedRateLimit(t, h, "pairing.lookup", subject, 30, 5*time.Minute, func() (int, http.Header, []byte) {
			return h.request(http.MethodPost, "/v1/pairing-requests/lookup", credential(workspace, "full"), "", map[string]any{"short_code": "23456789"})
		})
	})

	t.Run("claim 10 per 5 minutes by IP before claim parsing", func(t *testing.T) {
		h := newIntegrationHarness(t)
		pairingID := "00000000-0000-4000-8000-000000000341"
		assertFixedRateLimit(t, h, "pairing.claim.ip", "ip:192.0.2.20", 10, 5*time.Minute, func() (int, http.Header, []byte) {
			return h.request(http.MethodPost, "/v1/pairing-requests/"+pairingID+"/claim", "", "", map[string]any{"claim_secret": "deliberately-invalid"})
		})
	})

	t.Run("claim 10 per 5 minutes by pairing ID before claim parsing", func(t *testing.T) {
		h := newIntegrationHarness(t)
		pairingID := "00000000-0000-4000-8000-000000000342"
		assertFixedRateLimit(t, h, "pairing.claim.id", pairingID, 10, 5*time.Minute, func() (int, http.Header, []byte) {
			return h.request(http.MethodPost, "/v1/pairing-requests/"+pairingID+"/claim", "", "", map[string]any{"claim_secret": "deliberately-invalid"})
		})
	})

	t.Run("recovery 5 per 30 minutes by IP before Argon2id", func(t *testing.T) {
		h := newIntegrationHarness(t)
		workspace, _, _ := h.createWorkspace("Recovery IP Mac")
		corruptRecoveryVerifier(t, h, workspace.WorkspaceID)
		assertFixedRateLimit(t, h, "recovery.ip", "ip:192.0.2.20", 5, 30*time.Minute, func() (int, http.Header, []byte) {
			return h.request(http.MethodPost, "/v1/recoveries", "", h.nextKey(), map[string]any{
				"recovery_code": workspace.RecoveryCode, "device_name": "Blocked Recovery", "platform": "macos",
			})
		})
	})

	t.Run("recovery 5 per 30 minutes by locator before Argon2id", func(t *testing.T) {
		h := newIntegrationHarness(t)
		workspace, _, _ := h.createWorkspace("Recovery Locator Mac")
		workspaceID, locator, err := secure.RecoveryLocator(workspace.RecoveryCode)
		if err != nil {
			t.Fatal("parse generated recovery locator")
		}
		corruptRecoveryVerifier(t, h, workspace.WorkspaceID)
		assertFixedRateLimit(t, h, "recovery.locator", workspaceID+":"+locator, 5, 30*time.Minute, func() (int, http.Header, []byte) {
			return h.request(http.MethodPost, "/v1/recoveries", "", h.nextKey(), map[string]any{
				"recovery_code": workspace.RecoveryCode, "device_name": "Blocked Recovery", "platform": "macos",
			})
		})
	})
}

func TestIdempotentMutationReplayDoesNotConsumeQuotaIntegration(t *testing.T) {
	t.Run("workspace creation replay", func(t *testing.T) {
		h := newIntegrationHarness(t)
		key := h.nextKey()
		input := map[string]any{"device_name": "Replay Workspace", "platform": "macos"}
		status, _, firstBody := h.request(http.MethodPost, "/v1/workspaces", "", key, input)
		if status != http.StatusCreated {
			t.Fatalf("initial workspace status = %d", status)
		}
		before := rateLimitCount(t, h, "workspace.create", "ip:192.0.2.20")
		status, _, replayBody := h.request(http.MethodPost, "/v1/workspaces", "", key, input)
		if status != http.StatusCreated || !bytes.Equal(firstBody, replayBody) {
			t.Fatal("workspace replay status or body differs")
		}
		after := rateLimitCount(t, h, "workspace.create", "ip:192.0.2.20")
		if before != 1 || after != before {
			t.Fatalf("workspace quota before/after replay = %d/%d", before, after)
		}
	})

	t.Run("pairing creation replay", func(t *testing.T) {
		h := newIntegrationHarness(t)
		key := h.nextKey()
		input := map[string]any{
			"proposed_name": "Replay Pairing", "platform": "linux", "requested_scope": "connector",
		}
		status, _, firstBody := h.request(http.MethodPost, "/v1/pairing-requests", "", key, input)
		if status != http.StatusCreated {
			t.Fatalf("initial pairing status = %d", status)
		}
		before := rateLimitCount(t, h, "pairing.create", "ip:192.0.2.20")
		status, _, replayBody := h.request(http.MethodPost, "/v1/pairing-requests", "", key, input)
		if status != http.StatusCreated || !bytes.Equal(firstBody, replayBody) {
			t.Fatal("pairing replay status or body differs")
		}
		after := rateLimitCount(t, h, "pairing.create", "ip:192.0.2.20")
		if before != 1 || after != before {
			t.Fatalf("pairing quota before/after replay = %d/%d", before, after)
		}
	})

	t.Run("approval replay remains quota free", func(t *testing.T) {
		h := newIntegrationHarness(t)
		workspace, _, _ := h.createWorkspace("Approval Replay Mac")
		status, _, pairingBody := h.request(http.MethodPost, "/v1/pairing-requests", "", h.nextKey(), map[string]any{
			"proposed_name": "Approval Replay Joiner", "platform": "linux", "requested_scope": "connector",
		})
		if status != http.StatusCreated {
			t.Fatalf("pairing status = %d", status)
		}
		var pairing identity.PairingCreateResponse
		if err := json.Unmarshal(pairingBody, &pairing); err != nil {
			t.Fatalf("decode pairing response: %v", err)
		}
		var devicesBefore int
		if err := h.pool.QueryRow(context.Background(), `
select count(*) from devices where workspace_id = $1::uuid`, workspace.WorkspaceID).Scan(&devicesBefore); err != nil {
			t.Fatalf("count devices before approval: %v", err)
		}
		rowsBefore, requestsBefore := rateLimitTotals(t, h)
		key := h.nextKey()
		path := "/v1/pairing-requests/" + pairing.PairingID + "/approve"
		status, _, firstBody := h.request(http.MethodPost, path, credential(workspace, "full"), key, map[string]any{})
		if status != http.StatusOK {
			t.Fatalf("initial approval status = %d", status)
		}
		status, _, replayBody := h.request(http.MethodPost, path, credential(workspace, "full"), key, map[string]any{})
		if status != http.StatusOK || !bytes.Equal(firstBody, replayBody) {
			t.Fatal("approval replay status or body differs")
		}
		rowsAfter, requestsAfter := rateLimitTotals(t, h)
		if rowsAfter != rowsBefore || requestsAfter != requestsBefore {
			t.Fatalf("approval rate rows/requests before-after = %d/%d-%d/%d", rowsBefore, requestsBefore, rowsAfter, requestsAfter)
		}
		var devicesAfter int
		if err := h.pool.QueryRow(context.Background(), `
select count(*) from devices where workspace_id = $1::uuid`, workspace.WorkspaceID).Scan(&devicesAfter); err != nil {
			t.Fatalf("count devices after approval replay: %v", err)
		}
		if devicesAfter != devicesBefore+1 {
			t.Fatalf("devices before/after approval replay = %d/%d", devicesBefore, devicesAfter)
		}
	})

	t.Run("recovery replay", func(t *testing.T) {
		h := newIntegrationHarness(t)
		workspace, _, _ := h.createWorkspace("Recovery Replay Mac")
		workspaceID, locator, err := secure.RecoveryLocator(workspace.RecoveryCode)
		if err != nil {
			t.Fatal("parse generated recovery locator")
		}
		key := h.nextKey()
		input := map[string]any{
			"recovery_code": workspace.RecoveryCode, "device_name": "Recovery Replay Joiner", "platform": "macos",
		}
		status, _, firstBody := h.request(http.MethodPost, "/v1/recoveries", "", key, input)
		if status != http.StatusCreated {
			t.Fatalf("initial recovery status = %d", status)
		}
		ipBefore := rateLimitCount(t, h, "recovery.ip", "ip:192.0.2.20")
		locatorBefore := rateLimitCount(t, h, "recovery.locator", workspaceID+":"+locator)
		status, _, replayBody := h.request(http.MethodPost, "/v1/recoveries", "", key, input)
		if status != http.StatusCreated || !bytes.Equal(firstBody, replayBody) {
			t.Fatal("recovery replay status or body differs")
		}
		ipAfter := rateLimitCount(t, h, "recovery.ip", "ip:192.0.2.20")
		locatorAfter := rateLimitCount(t, h, "recovery.locator", workspaceID+":"+locator)
		if ipBefore != 1 || locatorBefore != 1 || ipAfter != ipBefore || locatorAfter != locatorBefore {
			t.Fatalf("recovery IP/locator quota before-after = %d/%d-%d/%d", ipBefore, locatorBefore, ipAfter, locatorAfter)
		}
	})
}

func TestDatabaseBackedReadinessIntegration(t *testing.T) {
	pool := testdb.New(t)
	handler := NewHandler(func(ctx context.Context) error { return database.Ready(ctx, pool) })
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("ready status = %d", ready.Code)
	}
	pool.Close()
	unavailable := httptest.NewRecorder()
	handler.ServeHTTP(unavailable, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if unavailable.Code != http.StatusServiceUnavailable || unavailable.Body.String() != "{\"status\":\"unavailable\"}\n" {
		t.Fatalf("closed pool status/body = %d/%q", unavailable.Code, unavailable.Body.String())
	}
}
