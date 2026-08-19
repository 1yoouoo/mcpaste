package peer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	httpLocalID  = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	httpPeerAID  = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	httpPeerBID  = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	httpPeerCID  = "dddddddd-dddd-dddd-dddd-dddddddddddd"
	httpLocalTok = "local-test-token"
)

func TestLocalContextRequiresLoopbackAndBearer(t *testing.T) {
	handler, store, _ := newHTTPTestHandler(t, httpLocalTok)
	if _, err := store.PublishLocal(LocalUpdate{Text: "local"}); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		remoteAddr string
		authorize  string
		wantCode   int
	}{
		{name: "tailnet remote", remoteAddr: "100.64.0.2:1234", authorize: "Bearer " + httpLocalTok, wantCode: http.StatusUnauthorized},
		{name: "no token", remoteAddr: "127.0.0.1:1234", wantCode: http.StatusUnauthorized},
		{name: "wrong token", remoteAddr: "127.0.0.1:1234", authorize: "Bearer wrong", wantCode: http.StatusUnauthorized},
		{name: "host and forwarded address do not help", remoteAddr: "203.0.113.8:1234", authorize: "Bearer " + httpLocalTok, wantCode: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := requestFrom("GET", "/v1/local/context", test.remoteAddr, test.authorize, nil)
			request.Host = "127.0.0.1:38421"
			request.Header.Set("X-Forwarded-For", "127.0.0.1")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantCode {
				t.Fatalf("status = %d, want %d", response.Code, test.wantCode)
			}
		})
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, requestFrom("GET", "/v1/local/context", "127.0.0.1:1234", "Bearer "+httpLocalTok, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("valid local request status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestPeerRoutesRequireCurrentTailnetAddress(t *testing.T) {
	handler, store, allowed := newHTTPTestHandler(t, httpLocalTok)
	if _, err := store.PublishLocal(LocalUpdate{Text: "peer context"}); err != nil {
		t.Fatal(err)
	}
	allowed.Replace([]netip.Addr{netip.MustParseAddr("100.64.0.2"), netip.MustParseAddr("2001:db8::2"), netip.MustParseAddr("127.0.0.1")})

	for _, test := range []struct {
		name       string
		remoteAddr string
		forwarded  string
		wantCode   int
	}{
		{name: "non allowlisted", remoteAddr: "203.0.113.8:1234", forwarded: "100.64.0.2", wantCode: http.StatusForbidden},
		{name: "loopback is not peer even when allowlisted", remoteAddr: "127.0.0.1:1234", wantCode: http.StatusForbidden},
		{name: "allowlisted IPv4", remoteAddr: "100.64.0.2:1234", wantCode: http.StatusOK},
		{name: "allowlisted IPv6", remoteAddr: "[2001:db8::2]:1234", wantCode: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := requestFrom("GET", "/v1/context", test.remoteAddr, "", nil)
			if test.forwarded != "" {
				request.Header.Set("X-Forwarded-For", test.forwarded)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantCode {
				t.Fatalf("status = %d, want %d", response.Code, test.wantCode)
			}
		})
	}
}

func TestLocalAuthEmptyTokenFailsClosedAndTokenComparisonIsFunctional(t *testing.T) {
	for _, configured := range []string{"", "correct-token"} {
		handler, store, _ := newHTTPTestHandler(t, configured)
		if _, err := store.PublishLocal(LocalUpdate{Text: "secret"}); err != nil {
			t.Fatal(err)
		}
		for _, supplied := range []string{"Bearer correct-token", "Bearer correct-toke", "Bearer correct-token-extra", "Bearer "} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, requestFrom("GET", "/v1/local/context", "127.0.0.1:1", supplied, nil))
			want := http.StatusUnauthorized
			if configured == "correct-token" && supplied == "Bearer correct-token" {
				want = http.StatusOK
			}
			if response.Code != want {
				t.Fatalf("configured %q supplied %q status = %d, want %d", configured, supplied, response.Code, want)
			}
		}
	}
}

func TestHealthResponseContainsOnlyApprovedMetadata(t *testing.T) {
	handler, store, _ := newHTTPTestHandler(t, "health-secret-token")
	assetData := []byte("asset-secret-content")
	asset := httpTestAsset(assetData, "image/png", 2, 3)
	if err := store.StageAsset(asset, assetData); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishLocal(LocalUpdate{Text: "health-text-secret", AssetDigests: []string{asset.Digest}}); err != nil {
		t.Fatal(err)
	}
	registry := handler.(*httpHandler).options.Registry
	if err := registry.Record(KnownPeer{
		DeviceID:    httpPeerAID,
		DisplayName: "registry-secret",
		Addresses:   []string{"100.64.0.2"},
		LastSeenAt:  time.Unix(2, 0),
	}); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, requestFrom("GET", "/v1/health", "100.64.0.2:1", "", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	wantKeys := map[string]bool{
		"protocol_version": true,
		"device_id":        true,
		"display_name":     true,
		"source_device_id": true,
		"revision":         true,
		"has_context":      true,
	}
	if len(fields) != len(wantKeys) {
		t.Fatalf("health keys = %#v, want %#v", fields, wantKeys)
	}
	for key := range fields {
		if !wantKeys[key] {
			t.Fatalf("unexpected health key %q", key)
		}
	}
	body := response.Body.String()
	for _, forbidden := range []string{"health-text-secret", "asset-secret-content", "health-secret-token", "registry-secret", "assets", "token", "registry"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("health body contains forbidden %q: %s", forbidden, body)
		}
	}
}

func TestHealthUsesPeerAndAuthenticatedLoopbackTrustDomains(t *testing.T) {
	handler, _, _ := newHTTPTestHandler(t, httpLocalTok)

	for _, test := range []struct {
		name       string
		method     string
		remoteAddr string
		authorize  string
		want       int
	}{
		{name: "allowlisted peer", method: http.MethodGet, remoteAddr: "100.64.0.2:1", want: http.StatusOK},
		{name: "authenticated loopback", method: http.MethodGet, remoteAddr: "127.0.0.1:1", authorize: "Bearer " + httpLocalTok, want: http.StatusOK},
		{name: "IPv6 loopback", method: http.MethodGet, remoteAddr: "[::1]:1", authorize: "Bearer " + httpLocalTok, want: http.StatusOK},
		{name: "IPv4 mapped loopback", method: http.MethodGet, remoteAddr: "[::ffff:127.0.0.1]:1", authorize: "Bearer " + httpLocalTok, want: http.StatusOK},
		{name: "unauthenticated loopback", method: http.MethodGet, remoteAddr: "127.0.0.1:1", want: http.StatusUnauthorized},
		{name: "wrong loopback token", method: http.MethodGet, remoteAddr: "127.0.0.1:1", authorize: "Bearer wrong", want: http.StatusUnauthorized},
		{name: "missing port fails closed", method: http.MethodGet, remoteAddr: "127.0.0.1", authorize: "Bearer " + httpLocalTok, want: http.StatusForbidden},
		{name: "malformed port fails closed", method: http.MethodGet, remoteAddr: "127.0.0.1:not-a-port", authorize: "Bearer " + httpLocalTok, want: http.StatusForbidden},
		{name: "overflow port fails closed", method: http.MethodGet, remoteAddr: "127.0.0.1:65536", authorize: "Bearer " + httpLocalTok, want: http.StatusForbidden},
		{name: "hostname fails closed", method: http.MethodGet, remoteAddr: "localhost:1", authorize: "Bearer " + httpLocalTok, want: http.StatusForbidden},
		{name: "zoned IPv6 fails closed", method: http.MethodGet, remoteAddr: "[::1%lo0]:1", authorize: "Bearer " + httpLocalTok, want: http.StatusForbidden},
		{name: "unauthenticated loopback wrong method", method: http.MethodPost, remoteAddr: "127.0.0.1:1", want: http.StatusUnauthorized},
		{name: "authenticated loopback wrong method", method: http.MethodPost, remoteAddr: "127.0.0.1:1", authorize: "Bearer " + httpLocalTok, want: http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, requestFrom(test.method, "/v1/health", test.remoteAddr, test.authorize, nil))
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestLocalContextServesOfflineReplicaButConnectorSnapshotRemainsOffline(t *testing.T) {
	handler, store, _ := newHTTPTestHandler(t, httpLocalTok)
	if _, err := store.PublishLocal(LocalUpdate{Text: "offline-replica"}); err != nil {
		t.Fatal(err)
	}
	if !store.SetSourceReachable(httpLocalID, false) {
		t.Fatal("SetSourceReachable() = false, want true")
	}
	if _, err := store.ConnectorSnapshot(); !errors.Is(err, ErrSourceOffline) {
		t.Fatalf("ConnectorSnapshot() error = %v, want %v", err, ErrSourceOffline)
	}

	localResponse := httptest.NewRecorder()
	handler.ServeHTTP(localResponse, requestFrom("GET", "/v1/local/context", "127.0.0.1:1", "Bearer "+httpLocalTok, nil))
	if localResponse.Code != http.StatusOK {
		t.Fatalf("local status = %d, want %d", localResponse.Code, http.StatusOK)
	}
	var local LocalContextResponse
	decodeTestJSON(t, localResponse, &local)
	if local.Text != "offline-replica" || local.SourceReachable || local.SyncState != SyncSourceOffline {
		t.Fatalf("offline local response = %+v", local)
	}

	peerResponse := httptest.NewRecorder()
	handler.ServeHTTP(peerResponse, requestFrom("GET", "/v1/context", "100.64.0.2:1", "", nil))
	if peerResponse.Code != http.StatusOK || !strings.Contains(peerResponse.Body.String(), "offline-replica") {
		t.Fatalf("peer response = %d %s", peerResponse.Code, peerResponse.Body.String())
	}
}

func TestAssetUploadValidatesAllHeadersBeforeReadingBody(t *testing.T) {
	handler, store, _ := newHTTPTestHandler(t, httpLocalTok)
	data := []byte("png-body")
	digest := httpDigest(data)

	tests := []struct {
		name                 string
		contentType          []string
		contentLengthHeaders []string
		contentLen           int64
		width                []string
		height               []string
		wantCode             int
	}{
		{name: "missing mime", contentLen: int64(len(data)), width: []string{"1"}, height: []string{"1"}, wantCode: http.StatusBadRequest},
		{name: "repeated mime", contentType: []string{"image/png", "image/png"}, contentLen: int64(len(data)), width: []string{"1"}, height: []string{"1"}, wantCode: http.StatusBadRequest},
		{name: "invalid mime", contentType: []string{"text/plain"}, contentLen: int64(len(data)), width: []string{"1"}, height: []string{"1"}, wantCode: http.StatusBadRequest},
		{name: "missing length", contentType: []string{"image/png"}, contentLen: -1, width: []string{"1"}, height: []string{"1"}, wantCode: http.StatusBadRequest},
		{name: "repeated length", contentType: []string{"image/png"}, contentLengthHeaders: []string{"8", "8"}, contentLen: int64(len(data)), width: []string{"1"}, height: []string{"1"}, wantCode: http.StatusBadRequest},
		{name: "zero length", contentType: []string{"image/png"}, contentLen: 0, width: []string{"1"}, height: []string{"1"}, wantCode: http.StatusBadRequest},
		{name: "too large length", contentType: []string{"image/png"}, contentLen: MaxAssetBytes + 1, width: []string{"1"}, height: []string{"1"}, wantCode: http.StatusRequestEntityTooLarge},
		{name: "missing width", contentType: []string{"image/png"}, contentLen: int64(len(data)), height: []string{"1"}, wantCode: http.StatusBadRequest},
		{name: "repeated width", contentType: []string{"image/png"}, contentLen: int64(len(data)), width: []string{"1", "1"}, height: []string{"1"}, wantCode: http.StatusBadRequest},
		{name: "zero width", contentType: []string{"image/png"}, contentLen: int64(len(data)), width: []string{"0"}, height: []string{"1"}, wantCode: http.StatusBadRequest},
		{name: "overflow width", contentType: []string{"image/png"}, contentLen: int64(len(data)), width: []string{"999999999999999999999999999999"}, height: []string{"1"}, wantCode: http.StatusBadRequest},
		{name: "missing height", contentType: []string{"image/png"}, contentLen: int64(len(data)), width: []string{"1"}, wantCode: http.StatusBadRequest},
		{name: "repeated height", contentType: []string{"image/png"}, contentLen: int64(len(data)), width: []string{"1"}, height: []string{"1", "1"}, wantCode: http.StatusBadRequest},
		{name: "zero height", contentType: []string{"image/png"}, contentLen: int64(len(data)), width: []string{"1"}, height: []string{"0"}, wantCode: http.StatusBadRequest},
		{name: "overflow height", contentType: []string{"image/png"}, contentLen: int64(len(data)), width: []string{"1"}, height: []string{"999999999999999999999999999999"}, wantCode: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spy := &httpSpyReader{data: data}
			request := requestFrom("PUT", "/v1/local/assets/"+digest, "127.0.0.1:1", "Bearer "+httpLocalTok, spy)
			request.ContentLength = test.contentLen
			setRepeatedHeader(request.Header, "Content-Type", test.contentType)
			setRepeatedHeader(request.Header, "Content-Length", test.contentLengthHeaders)
			setRepeatedHeader(request.Header, "X-MCPaste-Width", test.width)
			setRepeatedHeader(request.Header, "X-MCPaste-Height", test.height)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantCode {
				t.Fatalf("status = %d, want %d", response.Code, test.wantCode)
			}
			if spy.reads != 0 {
				t.Fatalf("body was read %d times for invalid headers", spy.reads)
			}
		})
	}

	valid := requestFrom("PUT", "/v1/local/assets/"+digest, "127.0.0.1:1", "Bearer "+httpLocalTok, bytes.NewReader(data))
	valid.ContentLength = int64(len(data))
	valid.Header.Set("Content-Type", "image/png")
	valid.Header.Set("X-MCPaste-Width", "2")
	valid.Header.Set("X-MCPaste-Height", "3")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, valid)
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid upload status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if _, err := store.PublishLocal(LocalUpdate{AssetDigests: []string{digest}}); err != nil {
		t.Fatalf("staged asset was not usable: %v", err)
	}
}

func TestAssetUploadRejectsShortExtraAndDigestMismatchBodies(t *testing.T) {
	handler, _, _ := newHTTPTestHandler(t, httpLocalTok)
	data := []byte("exact-body")
	digest := httpDigest(data)
	makeRequest := func(body []byte, declared int64, pathDigest string) *http.Request {
		request := requestFrom("PUT", "/v1/local/assets/"+pathDigest, "127.0.0.1:1", "Bearer "+httpLocalTok, bytes.NewReader(body))
		request.ContentLength = declared
		request.Header.Set("Content-Type", "image/jpeg")
		request.Header.Set("X-MCPaste-Width", "1")
		request.Header.Set("X-MCPaste-Height", "1")
		return request
	}

	for _, test := range []struct {
		name       string
		body       []byte
		declared   int64
		pathDigest string
		wantCode   int
	}{
		{name: "short", body: data[:len(data)-1], declared: int64(len(data)), pathDigest: digest, wantCode: http.StatusBadRequest},
		{name: "extra", body: append(append([]byte(nil), data...), 'x'), declared: int64(len(data)), pathDigest: digest, wantCode: http.StatusRequestEntityTooLarge},
		{name: "digest mismatch", body: []byte("different"), declared: int64(len("different")), pathDigest: digest, wantCode: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, makeRequest(test.body, test.declared, test.pathDigest))
			if response.Code != test.wantCode {
				t.Fatalf("status = %d, want %d", response.Code, test.wantCode)
			}
		})
	}
}

func TestLocalPublishPreservesExactTextAndRejectsUnknownTrailingAndOversize(t *testing.T) {
	handler, store, _ := newHTTPTestHandler(t, httpLocalTok)
	body := []byte(`{"text":"exact\r\ntext  ","asset_digests":[],"expected_revision":null}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localJSONRequest("PUT", "/v1/local/context", httpLocalTok, body))
	if response.Code != http.StatusOK {
		t.Fatalf("publish status = %d, want %d", response.Code, http.StatusOK)
	}
	manifest, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Text != "exact\r\ntext  " {
		t.Fatalf("published text = %q, want exact CRLF and spaces", manifest.Text)
	}
	var result PublicationResult
	decodeTestJSON(t, response, &result)
	if result.Revision != manifest.Revision || result.SyncState != SyncUpToDate {
		t.Fatalf("publication result = %+v, want revision %+v and up_to_date", result, manifest.Revision)
	}
	if response.Body.Len() > 1024 {
		t.Fatalf("publication response size = %d, want at most 1024", response.Body.Len())
	}
	var resultObject map[string]json.RawMessage
	decodeTestJSON(t, response, &resultObject)
	if len(resultObject) != 2 || resultObject["revision"] == nil || resultObject["sync_state"] == nil {
		t.Fatalf("publication response keys = %v, want revision and sync_state only", resultObject)
	}

	for _, test := range []struct {
		name     string
		body     []byte
		wantCode int
	}{
		{name: "omitted expected revision", body: []byte(`{"text":"safe","asset_digests":[]}`), wantCode: http.StatusBadRequest},
		{name: "unknown field", body: []byte(`{"text":"safe","asset_digests":[],"expected_revision":null,"secret":"do-not-echo"}`), wantCode: http.StatusBadRequest},
		{name: "trailing value", body: []byte(`{"text":"safe","asset_digests":[],"expected_revision":null} {"secret":"do-not-echo"}`), wantCode: http.StatusBadRequest},
		{name: "oversize text input", body: []byte(`{"text":"` + strings.Repeat("x", MaxTextBytes+1) + `","asset_digests":[],"expected_revision":null}`), wantCode: http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, localJSONRequest("PUT", "/v1/local/context", httpLocalTok, test.body))
			if response.Code != test.wantCode {
				t.Fatalf("status = %d, want %d", response.Code, test.wantCode)
			}
		})
	}
}

func TestLocalPublishReturnsValidatedPreCommitSyncState(t *testing.T) {
	for _, test := range []struct {
		name string
		pre  SyncState
		want SyncState
	}{
		{name: "up to date", pre: SyncUpToDate, want: SyncUpToDate},
		{name: "updating", pre: SyncUpdating, want: SyncUpdating},
		{name: "waiting", pre: SyncWaiting, want: SyncWaiting},
		{name: "offline becomes waiting for local source", pre: SyncSourceOffline, want: SyncWaiting},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, store, _ := newHTTPTestHandler(t, httpLocalTok)
			handler.(*httpHandler).options.SyncState = func() SyncState { return test.pre }
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, localJSONRequest(http.MethodPut, localContextRoute, httpLocalTok,
				[]byte(`{"text":"local","asset_digests":[],"expected_revision":null}`)))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
			}
			var result PublicationResult
			decodeTestJSON(t, response, &result)
			manifest, err := store.Manifest()
			if err != nil {
				t.Fatal(err)
			}
			if result.Revision != manifest.Revision || result.SyncState != test.want {
				t.Fatalf("result = %+v, want revision %+v state %q", result, manifest.Revision, test.want)
			}
		})
	}
}

func TestLocalPublishSyncStateFailureCannotCommit(t *testing.T) {
	for _, test := range []struct {
		name     string
		callback func() SyncState
	}{
		{name: "panic", callback: func() SyncState { panic("hidden") }},
		{name: "invalid", callback: func() SyncState { return SyncState("invalid") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, store, _ := newHTTPTestHandler(t, httpLocalTok)
			handler.(*httpHandler).options.SyncState = test.callback
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, localJSONRequest(http.MethodPut, localContextRoute, httpLocalTok,
				[]byte(`{"text":"must not commit","asset_digests":[],"expected_revision":null}`)))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
			}
			if _, err := store.Manifest(); !errors.Is(err, ErrNoContext) {
				t.Fatalf("Manifest() error = %v, want no committed context", err)
			}
		})
	}
}

func TestLocalPublishConflictKeepsCurrentAndStagedAssets(t *testing.T) {
	handler, store, _ := newHTTPTestHandler(t, httpLocalTok)
	initialResponse := httptest.NewRecorder()
	handler.ServeHTTP(initialResponse, localJSONRequest(http.MethodPut, localContextRoute, httpLocalTok,
		[]byte(`{"text":"current","asset_digests":[],"expected_revision":null}`)))
	if initialResponse.Code != http.StatusOK {
		t.Fatalf("initial status = %d; body=%s", initialResponse.Code, initialResponse.Body.String())
	}
	var initial PublicationResult
	decodeTestJSON(t, initialResponse, &initial)

	data := []byte{1, 2, 3}
	asset := testAsset(data, "image/png")
	if err := store.StageAsset(asset, data); err != nil {
		t.Fatal(err)
	}
	conflictResponse := httptest.NewRecorder()
	handler.ServeHTTP(conflictResponse, localJSONRequest(http.MethodPut, localContextRoute, httpLocalTok,
		[]byte(`{"text":"stale","asset_digests":["`+asset.Digest+`"],"expected_revision":null}`)))
	if conflictResponse.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, want %d", conflictResponse.Code, http.StatusConflict)
	}
	current, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if current.Text != "current" || current.Revision != initial.Revision {
		t.Fatalf("conflict changed current: %+v", current)
	}

	exactBody, err := json.Marshal(struct {
		Text             string    `json:"text"`
		AssetDigests     []string  `json:"asset_digests"`
		ExpectedRevision *Revision `json:"expected_revision"`
	}{Text: "winner", AssetDigests: []string{asset.Digest}, ExpectedRevision: &initial.Revision})
	if err != nil {
		t.Fatal(err)
	}
	exactResponse := httptest.NewRecorder()
	handler.ServeHTTP(exactResponse, localJSONRequest(http.MethodPut, localContextRoute, httpLocalTok, exactBody))
	if exactResponse.Code != http.StatusOK {
		t.Fatalf("exact status = %d, want %d; body=%s", exactResponse.Code, http.StatusOK, exactResponse.Body.String())
	}
	committed, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if committed.Text != "winner" || len(committed.Assets) != 1 || committed.Assets[0] != asset {
		t.Fatalf("exact commit lost staged asset: %+v", committed)
	}
}

func TestConcurrentLocalPublishRequestsWithSameExpectedRevisionHaveOneWinner(t *testing.T) {
	handler, store, _ := newHTTPTestHandler(t, httpLocalTok)
	initialResponse := httptest.NewRecorder()
	handler.ServeHTTP(initialResponse, localJSONRequest(http.MethodPut, localContextRoute, httpLocalTok,
		[]byte(`{"text":"base","asset_digests":[],"expected_revision":null}`)))
	if initialResponse.Code != http.StatusOK {
		t.Fatalf("initial status = %d; body=%s", initialResponse.Code, initialResponse.Body.String())
	}
	var initial PublicationResult
	decodeTestJSON(t, initialResponse, &initial)

	type outcome struct {
		text   string
		status int
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for _, text := range []string{"first", "second"} {
		go func() {
			body, err := json.Marshal(struct {
				Text             string    `json:"text"`
				AssetDigests     []string  `json:"asset_digests"`
				ExpectedRevision *Revision `json:"expected_revision"`
			}{Text: text, AssetDigests: []string{}, ExpectedRevision: &initial.Revision})
			if err != nil {
				outcomes <- outcome{text: text, status: 0}
				return
			}
			<-start
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, localJSONRequest(http.MethodPut, localContextRoute, httpLocalTok, body))
			outcomes <- outcome{text: text, status: response.Code}
		}()
	}
	close(start)
	var winner string
	var successes, conflicts int
	for i := 0; i < 2; i++ {
		result := <-outcomes
		switch result.status {
		case http.StatusOK:
			successes++
			winner = result.text
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("publish %q status = %d", result.text, result.status)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want one each", successes, conflicts)
	}
	current, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if current.Text != winner {
		t.Fatalf("current text = %q, want route winner %q", current.Text, winner)
	}
}

func TestJSONRoutesRequireOneApplicationJSONContentTypeBeforeReadingBody(t *testing.T) {
	handler, _, _ := newHTTPTestHandler(t, httpLocalTok)
	payload := []byte(`{"revision":{"wall_millis":10,"logical":0,"device_id":"` + httpPeerAID + `"}}`)
	tests := []struct {
		name       string
		method     string
		path       string
		remoteAddr string
		authorize  string
		values     []string
	}{
		{name: "local missing", method: http.MethodPut, path: "/v1/local/context", remoteAddr: "127.0.0.1:1", authorize: "Bearer " + httpLocalTok},
		{name: "local repeated", method: http.MethodPut, path: "/v1/local/context", remoteAddr: "127.0.0.1:1", authorize: "Bearer " + httpLocalTok, values: []string{"application/json", "application/json"}},
		{name: "local other media type", method: http.MethodPut, path: "/v1/local/context", remoteAddr: "127.0.0.1:1", authorize: "Bearer " + httpLocalTok, values: []string{"text/plain"}},
		{name: "local malformed", method: http.MethodPut, path: "/v1/local/context", remoteAddr: "127.0.0.1:1", authorize: "Bearer " + httpLocalTok, values: []string{"application/json; charset"}},
		{name: "peer missing", method: http.MethodPost, path: "/v1/announce", remoteAddr: "100.64.0.2:1"},
		{name: "peer repeated", method: http.MethodPost, path: "/v1/announce", remoteAddr: "100.64.0.2:1", values: []string{"application/json", "application/json"}},
		{name: "peer other media type", method: http.MethodPost, path: "/v1/announce", remoteAddr: "100.64.0.2:1", values: []string{"text/plain"}},
		{name: "peer malformed", method: http.MethodPost, path: "/v1/announce", remoteAddr: "100.64.0.2:1", values: []string{"application/json; charset"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spy := &httpSpyReader{data: payload}
			request := requestFrom(test.method, test.path, test.remoteAddr, test.authorize, spy)
			request.ContentLength = int64(len(payload))
			setRepeatedHeader(request.Header, "Content-Type", test.values)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
			if spy.reads != 0 {
				t.Fatalf("body was read %d times before rejecting Content-Type", spy.reads)
			}
		})
	}

	localBody := []byte(`{"text":"charset works","asset_digests":[],"expected_revision":null}`)
	local := localJSONRequest(http.MethodPut, "/v1/local/context", httpLocalTok, localBody)
	local.Header.Set("Content-Type", "application/json; charset=utf-8")
	localResponse := httptest.NewRecorder()
	handler.ServeHTTP(localResponse, local)
	if localResponse.Code != http.StatusOK {
		t.Fatalf("valid local media type status = %d, want %d", localResponse.Code, http.StatusOK)
	}

	announce := peerJSONRequest(http.MethodPost, "/v1/announce", Revision{WallMillis: 200, DeviceID: httpPeerAID})
	announce.Header.Set("Content-Type", "application/json; charset=utf-8")
	announceResponse := httptest.NewRecorder()
	handler.ServeHTTP(announceResponse, announce)
	if announceResponse.Code != http.StatusNoContent {
		t.Fatalf("valid peer media type status = %d, want %d", announceResponse.Code, http.StatusNoContent)
	}
}

func TestHTTPLimitErrorsMapTo413AndMalformedDigestRemains400(t *testing.T) {
	handler, store, _ := newHTTPTestHandler(t, httpLocalTok)
	tooMany := make([]string, MaxAssets+1)
	for index := range tooMany {
		tooMany[index] = strings.Repeat("a", 64)
	}
	body, err := json.Marshal(struct {
		AssetDigests     []string  `json:"asset_digests"`
		ExpectedRevision *Revision `json:"expected_revision"`
	}{AssetDigests: tooMany})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localJSONRequest(http.MethodPut, "/v1/local/context", httpLocalTok, body))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too-many-assets status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}

	textBody := []byte(`{"text":"` + strings.Repeat("x", MaxTextBytes+1) + `","asset_digests":[],"expected_revision":null}`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, localJSONRequest(http.MethodPut, "/v1/local/context", httpLocalTok, textBody))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized-text status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}

	if MaxBundleBytes%MaxAssetBytes != 0 {
		t.Fatal("test requires MaxBundleBytes divisible by MaxAssetBytes")
	}
	for index := 0; index < MaxBundleBytes/MaxAssetBytes; index++ {
		data := make([]byte, MaxAssetBytes)
		data[0] = byte(index + 1)
		if err := store.StageAsset(testAsset(data, "image/png"), data); err != nil {
			t.Fatalf("stage full bundle asset %d: %v", index, err)
		}
	}
	extra := []byte{99}
	extraRequest := requestFrom(http.MethodPut, "/v1/local/assets/"+httpDigest(extra), "127.0.0.1:1", "Bearer "+httpLocalTok, bytes.NewReader(extra))
	extraRequest.ContentLength = int64(len(extra))
	extraRequest.Header.Set("Content-Type", "image/png")
	extraRequest.Header.Set("X-MCPaste-Width", "1")
	extraRequest.Header.Set("X-MCPaste-Height", "1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, extraRequest)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("staging-quota status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}

	badDigest := []byte("digest mismatch")
	badRequest := requestFrom(http.MethodPut, "/v1/local/assets/"+strings.Repeat("a", 64), "127.0.0.1:1", "Bearer "+httpLocalTok, bytes.NewReader(badDigest))
	badRequest.ContentLength = int64(len(badDigest))
	badRequest.Header.Set("Content-Type", "image/png")
	badRequest.Header.Set("X-MCPaste-Width", "1")
	badRequest.Header.Set("X-MCPaste-Height", "1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, badRequest)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("malformed digest status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestLocalContextUpdateAcceptsMaximumEscapeHeavyTextAndRejectsDecodedOverflow(t *testing.T) {
	handler, store, _ := newHTTPTestHandler(t, httpLocalTok)
	text := strings.Repeat("\x01", MaxTextBytes)
	body, err := json.Marshal(localUpdateRequest{
		Text:             text,
		AssetDigests:     []string{},
		ExpectedRevision: json.RawMessage("null"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= MaxTextBytes+MaxAssets*64+8<<10 {
		t.Fatalf("escape-heavy body size = %d, test does not cross old wire bound", len(body))
	}
	if len(body) > MaxTextBytes*6+(64<<10) {
		t.Fatalf("escape-heavy body size = %d, exceeds sound wire bound", len(body))
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localJSONRequest(http.MethodPut, localContextRoute, httpLocalTok, body))
	if response.Code != http.StatusOK {
		t.Fatalf("maximum escape-heavy update status = %d, want %d", response.Code, http.StatusOK)
	}
	stored, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Text != text {
		t.Fatalf("stored text length = %d, want exact %d-byte text", len(stored.Text), len(text))
	}

	expectedJSON, err := json.Marshal(stored.Revision)
	if err != nil {
		t.Fatal(err)
	}
	overflowBody, err := json.Marshal(localUpdateRequest{
		Text:             strings.Repeat("x", MaxTextBytes+1),
		AssetDigests:     []string{},
		ExpectedRevision: expectedJSON,
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, localJSONRequest(http.MethodPut, localContextRoute, httpLocalTok, overflowBody))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("decoded text overflow status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
	unchanged, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Text != text || unchanged.Revision != stored.Revision {
		t.Fatal("decoded overflow mutated current context")
	}
}

func TestContextRoutesReturn404WithoutContext(t *testing.T) {
	handler, _, _ := newHTTPTestHandler(t, httpLocalTok)
	for _, test := range []struct {
		name       string
		path       string
		remoteAddr string
		authorize  string
	}{
		{name: "peer context", path: "/v1/context", remoteAddr: "100.64.0.2:1"},
		{name: "local context", path: "/v1/local/context", remoteAddr: "127.0.0.1:1", authorize: "Bearer " + httpLocalTok},
		{name: "peer asset", path: "/v1/context/assets/0", remoteAddr: "100.64.0.2:1"},
		{name: "local asset", path: "/v1/local/context/assets/0", remoteAddr: "127.0.0.1:1", authorize: "Bearer " + httpLocalTok},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, requestFrom(http.MethodGet, test.path, test.remoteAddr, test.authorize, nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
			}
		})
	}
}

func TestHTTPErrorBodiesAreExactGenericJSON(t *testing.T) {
	handler, store, _ := newHTTPTestHandler(t, httpLocalTok)
	current, err := store.PublishLocal(LocalUpdate{Text: "current"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		request  func() *http.Request
		wantCode int
		wantBody string
	}{
		{name: "400", request: func() *http.Request {
			return localJSONRequest(http.MethodPut, "/v1/local/context", httpLocalTok, []byte("{"))
		}, wantCode: http.StatusBadRequest, wantBody: `{"error":"bad request"}`},
		{name: "401", request: func() *http.Request {
			return requestFrom(http.MethodGet, "/v1/local/context", "127.0.0.1:1", "", nil)
		}, wantCode: http.StatusUnauthorized, wantBody: `{"error":"unauthorized"}`},
		{name: "403", request: func() *http.Request {
			return requestFrom(http.MethodGet, "/v1/health", "203.0.113.8:1", "", nil)
		}, wantCode: http.StatusForbidden, wantBody: `{"error":"forbidden"}`},
		{name: "404", request: func() *http.Request {
			return requestFrom(http.MethodGet, "/v1/context/assets/0", "100.64.0.2:1", "", nil)
		}, wantCode: http.StatusNotFound, wantBody: `{"error":"not found"}`},
		{name: "405", request: func() *http.Request {
			return requestFrom(http.MethodPost, "/v1/context", "100.64.0.2:1", "", nil)
		}, wantCode: http.StatusMethodNotAllowed, wantBody: `{"error":"method not allowed"}`},
		{name: "409", request: func() *http.Request {
			return peerJSONRequest(http.MethodPost, "/v1/announce", current.Revision)
		}, wantCode: http.StatusConflict, wantBody: `{"error":"conflict"}`},
		{name: "413", request: func() *http.Request {
			body := []byte(`{"text":"` + strings.Repeat("x", MaxTextBytes+1) + `","asset_digests":[],"expected_revision":null}`)
			return localJSONRequest(http.MethodPut, "/v1/local/context", httpLocalTok, body)
		}, wantCode: http.StatusRequestEntityTooLarge, wantBody: `{"error":"request too large"}`},
		{name: "503", request: func() *http.Request {
			handler.(*httpHandler).options.Announce = func(context.Context, Revision) error { return errors.New("hidden callback failure") }
			return peerJSONRequest(http.MethodPost, "/v1/announce", Revision{WallMillis: current.Revision.WallMillis + 1, DeviceID: httpPeerAID})
		}, wantCode: http.StatusServiceUnavailable, wantBody: `{"error":"service unavailable"}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, test.request())
			if response.Code != test.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantCode, response.Body.String())
			}
			if response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", response.Header().Get("Content-Type"))
			}
			if response.Body.String() != test.wantBody {
				t.Fatalf("body = %q, want %q", response.Body.String(), test.wantBody)
			}
		})
	}
}

func TestPeerAndLocalManifestAndOrderedIndexedAssets(t *testing.T) {
	handler, store, _ := newHTTPTestHandler(t, httpLocalTok)
	firstData := []byte{1, 2, 3}
	secondData := []byte{4, 5, 6, 7}
	first := httpTestAsset(firstData, "image/png", 1, 2)
	second := httpTestAsset(secondData, "image/jpeg", 3, 4)
	if err := store.StageAsset(first, firstData); err != nil {
		t.Fatal(err)
	}
	if err := store.StageAsset(second, secondData); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishLocal(LocalUpdate{Text: "ordered", AssetDigests: []string{first.Digest, second.Digest}}); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		path       string
		remoteAddr string
		authorize  string
		want       []byte
		mime       string
		digest     string
	}{
		{name: "peer first", path: "/v1/context/assets/0", remoteAddr: "100.64.0.2:1", want: firstData, mime: first.MIMEType, digest: first.Digest},
		{name: "peer second", path: "/v1/context/assets/1", remoteAddr: "100.64.0.2:1", want: secondData, mime: second.MIMEType, digest: second.Digest},
		{name: "local first", path: "/v1/local/context/assets/0", remoteAddr: "127.0.0.1:1", authorize: "Bearer " + httpLocalTok, want: firstData, mime: first.MIMEType, digest: first.Digest},
		{name: "local second", path: "/v1/local/context/assets/1", remoteAddr: "127.0.0.1:1", authorize: "Bearer " + httpLocalTok, want: secondData, mime: second.MIMEType, digest: second.Digest},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, requestFrom("GET", test.path, test.remoteAddr, test.authorize, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if !bytes.Equal(response.Body.Bytes(), test.want) {
				t.Fatalf("asset bytes = %v, want %v", response.Body.Bytes(), test.want)
			}
			if response.Header().Get("Content-Type") != test.mime {
				t.Fatalf("Content-Type = %q, want %q", response.Header().Get("Content-Type"), test.mime)
			}
			if response.Header().Get("Content-Length") != fmt.Sprint(len(test.want)) {
				t.Fatalf("Content-Length = %q, want %d", response.Header().Get("Content-Length"), len(test.want))
			}
			if response.Header().Get("X-MCPaste-SHA256") != test.digest {
				t.Fatalf("digest header = %q, want %q", response.Header().Get("X-MCPaste-SHA256"), test.digest)
			}
		})
	}

	for _, path := range []string{
		"/v1/context/assets/not-an-index",
		"/v1/context/assets/-1",
		"/v1/context/assets/0/extra",
		"/v1/local/context/assets/not-an-index",
		"/v1/local/context/assets/0/extra",
		"/v1/local/assets/ABC",
	} {
		response := httptest.NewRecorder()
		request := requestFrom("GET", path, "127.0.0.1:1", "Bearer "+httpLocalTok, nil)
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("path %q status = %d, want %d", path, response.Code, http.StatusNotFound)
		}
	}
}

func TestAnnounceNewerNoContextOlderEqualAndCallbackFailure(t *testing.T) {
	handler, store, _ := newHTTPTestHandler(t, httpLocalTok)
	var mu sync.Mutex
	var announced []Revision
	handler.(*httpHandler).options.Announce = func(_ context.Context, revision Revision) error {
		mu.Lock()
		defer mu.Unlock()
		announced = append(announced, revision)
		return nil
	}

	newRevision := Revision{WallMillis: 10, Logical: 0, DeviceID: httpPeerAID}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, peerJSONRequest("POST", "/v1/announce", newRevision))
	if response.Code != http.StatusNoContent {
		t.Fatalf("no-context announce status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if _, err := store.PublishLocal(LocalUpdate{Text: "current"}); err != nil {
		t.Fatal(err)
	}
	current, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, revision := range []Revision{current.Revision, {WallMillis: current.Revision.WallMillis - 1, DeviceID: httpPeerAID}} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, peerJSONRequest("POST", "/v1/announce", revision))
		if response.Code != http.StatusConflict {
			t.Fatalf("revision %+v status = %d, want %d", revision, response.Code, http.StatusConflict)
		}
	}
	newer := Revision{WallMillis: current.Revision.WallMillis + 1, DeviceID: httpPeerBID}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, peerJSONRequest("POST", "/v1/announce", newer))
	if response.Code != http.StatusNoContent {
		t.Fatalf("newer announce status = %d, want %d", response.Code, http.StatusNoContent)
	}

	handler.(*httpHandler).options.Announce = func(_ context.Context, _ Revision) error {
		return errors.New("callback secret should not leak")
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, peerJSONRequest("POST", "/v1/announce", Revision{WallMillis: newer.WallMillis + 1, DeviceID: httpPeerCID}))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("callback failure status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if strings.Contains(response.Body.String(), "callback secret") {
		t.Fatal("announce callback error leaked in response")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(announced) != 2 || announced[0] != newRevision || announced[1] != newer {
		t.Fatalf("announced revisions = %#v", announced)
	}
}

func TestDevicesResponseIsDeterministicDedupedAndCompact(t *testing.T) {
	handler, store, allowed := newHTTPTestHandler(t, httpLocalTok)
	reachable := handler.(*httpHandler).options.ReachablePeers
	registry := handler.(*httpHandler).options.Registry
	for _, peer := range []KnownPeer{
		{DeviceID: httpPeerAID, DisplayName: "Alpha", Addresses: []string{"::ffff:100.64.0.2"}, LastSeenAt: time.Date(2026, 8, 18, 1, 0, 0, 0, time.FixedZone("KST", 9*60*60))},
		{DeviceID: httpPeerBID, DisplayName: "Alpha", Addresses: []string{"2001:db8::3"}, LastSeenAt: time.Unix(3, 0)},
		{DeviceID: httpPeerCID, DisplayName: "Zulu", Addresses: []string{"100.64.0.4"}, LastSeenAt: time.Unix(4, 0)},
		{DeviceID: httpLocalID, DisplayName: "Duplicate local", Addresses: []string{"100.64.0.9"}, LastSeenAt: time.Unix(9, 0)},
	} {
		if err := registry.Record(peer); err != nil {
			t.Fatal(err)
		}
	}
	allowed.Replace([]netip.Addr{netip.MustParseAddr("100.64.0.2"), netip.MustParseAddr("2001:db8::3")})
	reachable.Replace([]netip.Addr{netip.MustParseAddr("2001:db8::3")})
	remote := testRemoteManifest(200, httpPeerAID, "remote-source")
	if err := store.AdoptRemote(remote, nil); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, requestFrom("GET", "/v1/local/devices", "127.0.0.1:1", "Bearer "+httpLocalTok, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	want := `{"devices":[{"id":"` + httpLocalID + `","display_name":"Local Device","reachable":true,"is_local":true,"is_source":false,"last_seen_at":"2026-08-18T00:00:01Z"},{"id":"` + httpPeerAID + `","display_name":"Alpha","reachable":false,"is_local":false,"is_source":true,"last_seen_at":"2026-08-17T16:00:00Z"},{"id":"` + httpPeerBID + `","display_name":"Alpha","reachable":true,"is_local":false,"is_source":false,"last_seen_at":"1970-01-01T00:00:03Z"},{"id":"` + httpPeerCID + `","display_name":"Zulu","reachable":false,"is_local":false,"is_source":false,"last_seen_at":"1970-01-01T00:00:04Z"}]}`
	if got := strings.TrimSpace(response.Body.String()); got != want {
		t.Fatalf("devices JSON = %s, want %s", got, want)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	var records []map[string]json.RawMessage
	if err := json.Unmarshal(envelope["devices"], &records); err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if len(record) != 6 {
			t.Fatalf("device keys = %#v, want exactly six keys", record)
		}
		for _, key := range []string{"id", "display_name", "reachable", "is_local", "is_source", "last_seen_at"} {
			if _, ok := record[key]; !ok {
				t.Fatalf("device missing key %q: %#v", key, record)
			}
		}
	}
}

func TestAllowedPeerIPsConcurrentReplaceAndContains(t *testing.T) {
	allowed := &AllowedPeerIPs{}
	a := netip.MustParseAddr("100.64.0.2")
	b := netip.MustParseAddr("::ffff:100.64.0.3")
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for iteration := 0; iteration < 200; iteration++ {
				if (worker+iteration)%2 == 0 {
					allowed.Replace([]netip.Addr{a, b, netip.MustParseAddr("fe80::1%en0")})
				} else {
					allowed.Replace([]netip.Addr{netip.MustParseAddr("2001:db8::4")})
				}
				_ = allowed.Contains(a)
				_ = allowed.Contains(netip.MustParseAddr("::ffff:100.64.0.3"))
			}
		}(worker)
	}
	wg.Wait()
	allowed.Replace([]netip.Addr{b})
	if !allowed.Contains(netip.MustParseAddr("100.64.0.3")) {
		t.Fatal("Replace did not unmap IPv4-mapped address")
	}
	if allowed.Contains(netip.MustParseAddr("fe80::1%en0")) {
		t.Fatal("zoned address was accepted")
	}
}

func TestHTTPServerUsesExactTimeouts(t *testing.T) {
	handler, _, _ := newHTTPTestHandler(t, httpLocalTok)
	server := NewHTTPServer("127.0.0.1:38421", handler)
	if server.Addr != "127.0.0.1:38421" || server.Handler != handler {
		t.Fatalf("server address/handler = %q/%T", server.Addr, server.Handler)
	}
	if server.ReadHeaderTimeout != 2*time.Second || server.ReadTimeout != 10*time.Second || server.WriteTimeout != 30*time.Second || server.IdleTimeout != 30*time.Second {
		t.Fatalf("server timeouts = header %s read %s write %s idle %s", server.ReadHeaderTimeout, server.ReadTimeout, server.WriteTimeout, server.IdleTimeout)
	}
}

func TestResponseErrorsDoNotLeakSuppliedValues(t *testing.T) {
	handler, _, _ := newHTTPTestHandler(t, httpLocalTok)
	secret := "supplied-secret-content"
	digest := strings.Repeat("a", 64)
	request := requestFrom("PUT", "/v1/local/assets/"+digest, "127.0.0.1:1", "Bearer "+httpLocalTok, bytes.NewBufferString(secret))
	request.ContentLength = int64(len(secret))
	request.Header.Set("Content-Type", "image/png")
	request.Header.Set("X-MCPaste-Width", "1")
	request.Header.Set("X-MCPaste-Height", "1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	for _, forbidden := range []string{secret, digest, "invalid context asset", "/v1/local/assets"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("error response leaked %q: %s", forbidden, response.Body.String())
		}
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("error Content-Type = %q, want application/json", response.Header().Get("Content-Type"))
	}
}

func TestHTTPMethodsPathsAndNilDependenciesFailClosed(t *testing.T) {
	handler, store, _ := newHTTPTestHandler(t, httpLocalTok)
	if _, err := store.PublishLocal(LocalUpdate{Text: "method"}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method string
		path   string
		want   int
	}{
		{method: "POST", path: "/v1/context", want: http.StatusMethodNotAllowed},
		{method: "GET", path: "/v1/context/", want: http.StatusNotFound},
		{method: "GET", path: "/v1/context/extra", want: http.StatusNotFound},
		{method: "GET", path: "/v1/local/assets/short", want: http.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, requestFrom(test.method, test.path, "100.64.0.2:1", "", nil))
		if response.Code != test.want {
			t.Fatalf("%s %s status = %d, want %d", test.method, test.path, response.Code, test.want)
		}
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("nil dependency handler panicked: %v", recovered)
		}
	}()
	response := httptest.NewRecorder()
	NewHandler(HandlerOptions{}).ServeHTTP(response, requestFrom("GET", "/v1/health", "100.64.0.2:1", "", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("nil dependency status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestHTTPStoreManifestAndIndexedAssetAccessorsCopyOnlyTheirResults(t *testing.T) {
	store := newTestStore(t, httpLocalID, 100)
	firstData := []byte{1, 2, 3}
	secondData := []byte{4, 5, 6}
	first := httpTestAsset(firstData, "image/png", 1, 1)
	second := httpTestAsset(secondData, "image/jpeg", 2, 2)
	for _, asset := range []struct {
		manifest AssetManifest
		data     []byte
	}{
		{manifest: first, data: firstData},
		{manifest: second, data: secondData},
	} {
		if err := store.StageAsset(asset.manifest, asset.data); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.PublishLocal(LocalUpdate{Text: "context", AssetDigests: []string{first.Digest, second.Digest}}); err != nil {
		t.Fatal(err)
	}

	manifest, reachable, err := store.httpManifest()
	if err != nil || !reachable || manifest.Text != "context" || len(manifest.Assets) != 2 {
		t.Fatalf("httpManifest() = %#v, %t, %v", manifest, reachable, err)
	}
	manifest.Assets[0].Digest = "changed"
	stored, err := store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Assets[0].Digest != first.Digest {
		t.Fatalf("manifest mutation changed store asset = %q, want %q", stored.Assets[0].Digest, first.Digest)
	}

	asset, data, ok, err := store.httpAsset(1)
	if err != nil || !ok || asset != second || !bytes.Equal(data, secondData) {
		t.Fatalf("httpAsset(1) = %#v, %v, %t, %v", asset, data, ok, err)
	}
	data[0] = 0
	asset.Digest = "changed"
	assetAgain, dataAgain, ok, err := store.httpAsset(1)
	if err != nil || !ok || assetAgain != second || !bytes.Equal(dataAgain, secondData) {
		t.Fatalf("httpAsset(1) after mutation = %#v, %v, %t, %v", assetAgain, dataAgain, ok, err)
	}
}

func TestLocalAuthRetainsOnlyFixedSizeBearerDigest(t *testing.T) {
	handler, store, _ := newHTTPTestHandler(t, httpLocalTok)
	if _, err := store.PublishLocal(LocalUpdate{Text: "secret"}); err != nil {
		t.Fatal(err)
	}
	internal := handler.(*httpHandler)
	if internal.options.LocalToken != "" {
		t.Fatal("handler retained plaintext local token")
	}
	wantDigest := sha256.Sum256([]byte("Bearer " + httpLocalTok))
	if internal.localTokenDigest != wantDigest {
		t.Fatal("handler bearer digest does not match configured token")
	}

	for _, test := range []struct {
		name   string
		values []string
		want   int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "repeated", values: []string{"Bearer " + httpLocalTok, "Bearer " + httpLocalTok}, want: http.StatusUnauthorized},
		{name: "short", values: []string{"Bearer " + httpLocalTok[:len(httpLocalTok)-1]}, want: http.StatusUnauthorized},
		{name: "long", values: []string{"Bearer " + httpLocalTok + "-extra"}, want: http.StatusUnauthorized},
		{name: "wrong", values: []string{"Bearer another-token"}, want: http.StatusUnauthorized},
		{name: "valid", values: []string{"Bearer " + httpLocalTok}, want: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := requestFrom(http.MethodGet, localContextRoute, "127.0.0.1:1", "", nil)
			setRepeatedHeader(request.Header, "Authorization", test.values)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
}

func TestAnnounceFutureRevisionBoundarySkipsCallback(t *testing.T) {
	handler, _, _ := newHTTPTestHandler(t, httpLocalTok)
	var mu sync.Mutex
	calls := 0
	handler.(*httpHandler).options.Announce = func(context.Context, Revision) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return nil
	}
	for _, test := range []struct {
		name string
		wall int64
		want int
	}{
		{name: "exactly 24 hours", wall: 100 + maxFutureRevisionMillis, want: http.StatusNoContent},
		{name: "one millisecond beyond", wall: 100 + maxFutureRevisionMillis + 1, want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, peerJSONRequest(http.MethodPost, "/v1/announce", Revision{WallMillis: test.wall, DeviceID: httpPeerAID}))
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("callback calls = %d, want 1", calls)
	}
}

func TestAnnounceCallbackIsSingleFlightAndBoundsTimeouts(t *testing.T) {
	t.Run("same revision coalesces and a different revision is busy", func(t *testing.T) {
		handler, _, _ := newHTTPTestHandler(t, httpLocalTok)
		var mu sync.Mutex
		calls := 0
		active := 0
		maxActive := 0
		started := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(release) })
		handler.(*httpHandler).options.Announce = func(context.Context, Revision) error {
			mu.Lock()
			calls++
			active++
			if active > maxActive {
				maxActive = active
			}
			if calls == 1 {
				close(started)
			}
			mu.Unlock()
			<-release
			mu.Lock()
			active--
			mu.Unlock()
			return nil
		}

		revision := Revision{WallMillis: 101, DeviceID: httpPeerAID}
		first := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, peerJSONRequest(http.MethodPost, "/v1/announce", revision))
			first <- response
		}()
		<-started
		same := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, peerJSONRequest(http.MethodPost, "/v1/announce", revision))
			same <- response
		}()
		joinedDeadline := time.NewTimer(300 * time.Millisecond)
		for {
			internal := handler.(*httpHandler)
			internal.announceMu.Lock()
			joined := internal.announceActive != nil && internal.announceActive.revision == revision && internal.announceActive.followers == 1
			internal.announceMu.Unlock()
			if joined {
				break
			}
			select {
			case <-joinedDeadline.C:
				t.Fatal("same revision request did not join the active announce call")
			default:
				runtime.Gosched()
			}
		}
		if !joinedDeadline.Stop() {
			select {
			case <-joinedDeadline.C:
			default:
			}
		}
		busy := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, peerJSONRequest(http.MethodPost, "/v1/announce", Revision{WallMillis: 102, DeviceID: httpPeerBID}))
			busy <- response
		}()

		select {
		case response := <-busy:
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("different revision status = %d, want %d", response.Code, http.StatusServiceUnavailable)
			}
		case <-time.After(300 * time.Millisecond):
			t.Fatal("different revision did not return while callback was active")
		}
		releaseOnce.Do(func() { close(release) })
		for _, response := range []*httptest.ResponseRecorder{<-first, <-same} {
			if response.Code != http.StatusNoContent {
				t.Fatalf("same revision status = %d, want %d", response.Code, http.StatusNoContent)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if calls != 1 || maxActive != 1 {
			t.Fatalf("callback calls/max active = %d/%d, want 1/1", calls, maxActive)
		}
	})

	t.Run("callback panic and deadline return service unavailable", func(t *testing.T) {
		handler, _, _ := newHTTPTestHandler(t, httpLocalTok)
		handler.(*httpHandler).options.Announce = func(context.Context, Revision) error { panic("callback panic") }
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, peerJSONRequest(http.MethodPost, "/v1/announce", Revision{WallMillis: 101, DeviceID: httpPeerAID}))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("panic status = %d, want %d", response.Code, http.StatusServiceUnavailable)
		}

		deadlineReached := make(chan struct{})
		handler.(*httpHandler).options.Announce = func(ctx context.Context, _ Revision) error {
			<-ctx.Done()
			close(deadlineReached)
			return ctx.Err()
		}
		requestContext, cancel := context.WithCancel(context.Background())
		defer cancel()
		request := peerJSONRequest(http.MethodPost, "/v1/announce", Revision{WallMillis: 102, DeviceID: httpPeerBID}).WithContext(requestContext)
		finished := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			finished <- response
		}()
		select {
		case response := <-finished:
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("deadline status = %d, want %d", response.Code, http.StatusServiceUnavailable)
			}
		case <-time.After(2300 * time.Millisecond):
			cancel()
			<-finished
			t.Fatal("callback context did not receive the two-second deadline")
		}
		select {
		case <-deadlineReached:
		case <-time.After(200 * time.Millisecond):
			t.Fatal("callback did not observe cancellation")
		}
	})
}

func TestAnnounceCancellationIgnoringCallbackRemainsSingleFlightAfterTimeout(t *testing.T) {
	handler, _, _ := newHTTPTestHandler(t, httpLocalTok)
	started := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan Revision, 2)
	var mu sync.Mutex
	calls := 0
	handler.(*httpHandler).options.Announce = func(_ context.Context, revision Revision) error {
		mu.Lock()
		calls++
		if calls == 1 {
			close(started)
		}
		mu.Unlock()
		<-release
		returned <- revision
		return nil
	}

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, peerJSONRequest(http.MethodPost, "/v1/announce", Revision{WallMillis: 101, DeviceID: httpPeerAID}))
		first <- response
	}()
	<-started
	internal := handler.(*httpHandler)
	internal.announceMu.Lock()
	call := internal.announceActive
	internal.announceMu.Unlock()
	if call == nil {
		t.Fatal("announce call was not active")
	}
	select {
	case response := <-first:
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("timed out callback status = %d, want %d", response.Code, http.StatusServiceUnavailable)
		}
	case <-time.After(2300 * time.Millisecond):
		t.Fatal("callback waiter did not time out after two seconds")
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, peerJSONRequest(http.MethodPost, "/v1/announce", Revision{WallMillis: 102, DeviceID: httpPeerBID}))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("different revision after timeout status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	close(release)
	select {
	case <-call.callbackDone:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("cancellation-ignoring callback did not finish after release")
	}
	if got := <-returned; got.DeviceID != httpPeerAID {
		t.Fatalf("released callback revision = %+v", got)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, peerJSONRequest(http.MethodPost, "/v1/announce", Revision{WallMillis: 103, DeviceID: httpPeerCID}))
	if response.Code != http.StatusNoContent {
		t.Fatalf("different revision after callback return status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if got := <-returned; got.DeviceID != httpPeerCID {
		t.Fatalf("reusable callback revision = %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("callback calls = %d, want 2", calls)
	}
}

func TestAnnounceCallPublishesOneSharedDeadlineResult(t *testing.T) {
	handler, _, _ := newHTTPTestHandler(t, httpLocalTok)
	started := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	handler.(*httpHandler).options.Announce = func(ctx context.Context, _ Revision) error {
		mu.Lock()
		calls++
		if calls == 1 {
			close(started)
		}
		mu.Unlock()
		<-ctx.Done()
		return nil
	}

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, peerJSONRequest(http.MethodPost, "/v1/announce", Revision{WallMillis: 101, DeviceID: httpPeerAID}))
		first <- response
	}()
	<-started
	internal := handler.(*httpHandler)
	internal.announceMu.Lock()
	call := internal.announceActive
	internal.announceMu.Unlock()
	if call == nil {
		t.Fatal("announce call was not active")
	}
	same := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, peerJSONRequest(http.MethodPost, "/v1/announce", Revision{WallMillis: 101, DeviceID: httpPeerAID}))
		same <- response
	}()
	select {
	case <-call.publicDone:
	case <-time.After(2300 * time.Millisecond):
		t.Fatal("shared announce result did not resolve at the deadline")
	}
	for _, response := range []*httptest.ResponseRecorder{<-first, <-same} {
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("deadline status = %d, want %d", response.Code, http.StatusServiceUnavailable)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("callback calls = %d, want 1", calls)
	}
}

func TestAnnounceSuccessfulResultImmediatelyReleasesActiveSlot(t *testing.T) {
	handler, _, _ := newHTTPTestHandler(t, httpLocalTok)
	var mu sync.Mutex
	calls := 0
	handler.(*httpHandler).options.Announce = func(context.Context, Revision) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil
	}

	for attempt := int64(0); attempt < 1000; attempt++ {
		first := make(chan *httptest.ResponseRecorder, 1)
		go func(attempt int64) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, peerJSONRequest(http.MethodPost, "/v1/announce", Revision{WallMillis: 100 + attempt*2, DeviceID: httpPeerAID}))
			first <- response
		}(attempt)
		response := <-first
		if response.Code != http.StatusNoContent {
			t.Fatalf("first revision status = %d, want %d", response.Code, http.StatusNoContent)
		}

		response = httptest.NewRecorder()
		handler.ServeHTTP(response, peerJSONRequest(http.MethodPost, "/v1/announce", Revision{WallMillis: 101 + attempt*2, DeviceID: httpPeerBID}))
		if response.Code != http.StatusNoContent {
			t.Fatalf("different revision after successful result status = %d, want %d on attempt %d", response.Code, http.StatusNoContent, attempt)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2000 {
		t.Fatalf("callback calls = %d, want 2000", calls)
	}
}

func TestLocalContextRequiresAnAllowedPanicSafeSyncState(t *testing.T) {
	handler, store, _ := newHTTPTestHandler(t, httpLocalTok)
	if _, err := store.PublishLocal(LocalUpdate{Text: "context"}); err != nil {
		t.Fatal(err)
	}
	internal := handler.(*httpHandler)
	get := func() *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, requestFrom(http.MethodGet, localContextRoute, "127.0.0.1:1", "Bearer "+httpLocalTok, nil))
		return response
	}
	for _, test := range []struct {
		callback SyncState
		want     SyncState
	}{
		{callback: SyncUpToDate, want: SyncUpToDate},
		{callback: SyncUpdating, want: SyncUpdating},
		{callback: SyncWaiting, want: SyncWaiting},
		{callback: SyncSourceOffline, want: SyncWaiting},
	} {
		internal.options.SyncState = func() SyncState { return test.callback }
		response := get()
		if response.Code != http.StatusOK {
			t.Fatalf("sync state %q status = %d, want %d", test.callback, response.Code, http.StatusOK)
		}
		var body LocalContextResponse
		decodeTestJSON(t, response, &body)
		if body.SyncState != test.want {
			t.Fatalf("sync state for reachable source/callback %q = %q, want %q", test.callback, body.SyncState, test.want)
		}
	}

	for _, test := range []struct {
		name     string
		callback func() SyncState
	}{
		{name: "missing"},
		{name: "panic", callback: func() SyncState { panic("sync state panic") }},
		{name: "invalid", callback: func() SyncState { return SyncState("unexpected") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			internal.options.SyncState = test.callback
			response := get()
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
			}
		})
	}

	if !store.SetSourceReachable(httpLocalID, false) {
		t.Fatal("SetSourceReachable() = false")
	}
	calls := 0
	internal.options.SyncState = func() SyncState {
		calls++
		return SyncSourceOffline
	}
	response := get()
	if response.Code != http.StatusOK {
		t.Fatalf("offline status = %d, want %d", response.Code, http.StatusOK)
	}
	var offline LocalContextResponse
	decodeTestJSON(t, response, &offline)
	if offline.SourceReachable || offline.SyncState != SyncSourceOffline || calls != 1 {
		t.Fatalf("offline response/calls = %+v/%d", offline, calls)
	}
	for _, test := range []struct {
		name     string
		callback func() SyncState
	}{
		{name: "offline missing"},
		{name: "offline panic", callback: func() SyncState { panic("offline sync state panic") }},
		{name: "offline invalid", callback: func() SyncState { return SyncState("unexpected") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			internal.options.SyncState = test.callback
			response := get()
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
			}
		})
	}
}

func TestKnownRouteAuthenticationPrecedesMethodAndConfiguration(t *testing.T) {
	handler, _, allowed := newHTTPTestHandler(t, httpLocalTok)
	localAssetPath := localAssetsBase + strings.Repeat("a", sha256DigestLength)
	for _, test := range []struct {
		name       string
		request    *http.Request
		wantStatus int
	}{
		{name: "unauthenticated local wrong method", request: requestFrom(http.MethodGet, localAssetPath, "127.0.0.1:1", "", nil), wantStatus: http.StatusUnauthorized},
		{name: "authenticated local wrong method", request: requestFrom(http.MethodGet, localAssetPath, "127.0.0.1:1", "Bearer "+httpLocalTok, nil), wantStatus: http.StatusMethodNotAllowed},
		{name: "non allowlisted peer wrong method", request: requestFrom(http.MethodPost, "/v1/health", "203.0.113.8:1", "", nil), wantStatus: http.StatusForbidden},
		{name: "allowlisted peer wrong method", request: requestFrom(http.MethodPost, "/v1/health", "100.64.0.2:1", "", nil), wantStatus: http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, test.request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
		})
	}

	brokenLocal := NewHandler(HandlerOptions{LocalToken: httpLocalTok})
	response := httptest.NewRecorder()
	brokenLocal.ServeHTTP(response, requestFrom(http.MethodGet, localContextRoute, "127.0.0.1:1", "", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated misconfigured local status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	response = httptest.NewRecorder()
	brokenLocal.ServeHTTP(response, requestFrom(http.MethodGet, localContextRoute, "127.0.0.1:1", "Bearer "+httpLocalTok, nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("authenticated misconfigured local status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}

	brokenAllowed := &AllowedPeerIPs{}
	brokenAllowed.Replace([]netip.Addr{netip.MustParseAddr("100.64.0.2")})
	brokenPeer := NewHandler(HandlerOptions{AllowedPeers: brokenAllowed})
	response = httptest.NewRecorder()
	brokenPeer.ServeHTTP(response, requestFrom(http.MethodGet, "/v1/health", "203.0.113.8:1", "", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("non allowlisted misconfigured peer status = %d, want %d", response.Code, http.StatusForbidden)
	}
	response = httptest.NewRecorder()
	brokenPeer.ServeHTTP(response, requestFrom(http.MethodGet, "/v1/health", "100.64.0.2:1", "", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("allowlisted misconfigured peer status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}

	if !allowed.Contains(netip.MustParseAddr("100.64.0.2")) {
		t.Fatal("test handler peer was not allowlisted")
	}
}

func newHTTPTestHandler(t *testing.T, token string) (http.Handler, *Store, *AllowedPeerIPs) {
	t.Helper()
	store := newTestStore(t, httpLocalID, 100)
	registry := NewRegistry(filepath.Join(t.TempDir(), "known-peers.json"))
	allowed := &AllowedPeerIPs{}
	allowed.Replace([]netip.Addr{netip.MustParseAddr("100.64.0.2")})
	reachable := &AllowedPeerIPs{}
	options := HandlerOptions{
		Store:          store,
		Registry:       registry,
		LocalDevice:    KnownPeer{DeviceID: httpLocalID, DisplayName: "Local Device", LastSeenAt: time.Date(2026, 8, 18, 0, 0, 1, 0, time.UTC)},
		LocalToken:     token,
		AllowedPeers:   allowed,
		ReachablePeers: reachable,
		SyncState:      func() SyncState { return SyncUpToDate },
		Announce:       func(context.Context, Revision) error { return nil },
	}
	return NewHandler(options), store, allowed
}

func requestFrom(method, path, remoteAddr, authorization string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, "http://mcpaste.local"+path, body)
	request.RemoteAddr = remoteAddr
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	return request
}

func localJSONRequest(method, path, token string, body []byte) *http.Request {
	request := requestFrom(method, path, "127.0.0.1:1", "Bearer "+token, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.ContentLength = int64(len(body))
	return request
}

func peerJSONRequest(method, path string, revision Revision) *http.Request {
	body, err := json.Marshal(struct {
		Revision Revision `json:"revision"`
	}{Revision: revision})
	if err != nil {
		panic(err)
	}
	request := requestFrom(method, path, "100.64.0.2:1", "", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.ContentLength = int64(len(body))
	return request
}

func decodeTestJSON(t *testing.T, response *httptest.ResponseRecorder, value any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), value); err != nil {
		t.Fatalf("decode JSON: %v; body=%s", err, response.Body.String())
	}
}

func setRepeatedHeader(header http.Header, name string, values []string) {
	header.Del(name)
	for _, value := range values {
		header.Add(name, value)
	}
}

func httpTestAsset(data []byte, mime string, width, height int) AssetManifest {
	return AssetManifest{Digest: httpDigest(data), MIMEType: mime, Width: width, Height: height, ByteSize: len(data)}
}

func httpDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

type httpSpyReader struct {
	data  []byte
	reads int
}

func (reader *httpSpyReader) Read(data []byte) (int, error) {
	reader.reads++
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	n := copy(data, reader.data)
	reader.data = reader.data[n:]
	return n, nil
}
