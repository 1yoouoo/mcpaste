package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/internal/peer"
)

const localTestToken = "example-local-token-not-real"

func TestLocalClientReadsExactTextAndOrderedAssets(t *testing.T) {
	first := []byte("png-image-bytes")
	second := []byte("jpeg-image-bytes")
	manifest := localTestManifest("first line\r\nsecond line \t\r\n", first, second)
	var manifestCalls atomic.Int32
	var assetCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertLocalRequest(t, r)
		switch r.URL.Path {
		case "/v1/local/context":
			manifestCalls.Add(1)
			writeLocalJSON(t, w, http.StatusOK, manifest)
		case "/v1/local/context/assets/0":
			assetCalls.Add(1)
			writeLocalAsset(w, manifest.Assets[0], first)
		case "/v1/local/context/assets/1":
			assetCalls.Add(1)
			writeLocalAsset(w, manifest.Assets[1], second)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewLocalClient(Credential{Endpoint: server.URL, Token: localTestToken}, server.Client())
	if err != nil {
		t.Fatalf("NewLocalClient() error = %v", err)
	}
	contextValue, err := client.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if contextValue.Manifest.Text != manifest.Text {
		t.Fatalf("text = %q, want exact %q", contextValue.Manifest.Text, manifest.Text)
	}
	if len(contextValue.Assets) != 2 || string(contextValue.Assets[0]) != string(first) || string(contextValue.Assets[1]) != string(second) {
		t.Fatalf("ordered assets = %q", contextValue.Assets)
	}
	if manifestCalls.Load() != 1 || assetCalls.Load() != 2 {
		t.Fatalf("manifest/asset calls = %d/%d, want 1/2", manifestCalls.Load(), assetCalls.Load())
	}
}

func TestLocalClientAcceptsMaxTextWithWorstCaseJSONEscaping(t *testing.T) {
	text := strings.Repeat("\x01", peer.MaxTextBytes)
	manifest := localTestManifest(text)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertLocalRequest(t, r)
		writeLocalJSON(t, w, http.StatusOK, manifest)
	}))
	defer server.Close()

	client, err := NewLocalClient(Credential{Endpoint: server.URL, Token: localTestToken}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	contextValue, err := client.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(contextValue.Manifest.Text) != peer.MaxTextBytes || contextValue.Manifest.Text[0] != '\x01' || contextValue.Manifest.Text[len(contextValue.Manifest.Text)-1] != '\x01' {
		t.Fatal("Read() did not preserve the maximum heavily escaped text")
	}
}

func TestLocalClientRefusesOfflineSourceBeforeFetchingAssets(t *testing.T) {
	asset := []byte("stale-image")
	manifest := localTestManifest("stale text", asset)
	manifest.SourceReachable = false
	manifest.SyncState = peer.SyncSourceOffline
	var assetCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertLocalRequest(t, r)
		if r.URL.Path == "/v1/local/context" {
			writeLocalJSON(t, w, http.StatusOK, manifest)
			return
		}
		assetCalls.Add(1)
		http.Error(w, "stale asset requested", http.StatusInternalServerError)
	}))
	defer server.Close()

	client, err := NewLocalClient(Credential{Endpoint: server.URL, Token: localTestToken}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Read(context.Background())
	if !errors.Is(err, ErrSourceOffline) {
		t.Fatalf("Read() error = %v, want ErrSourceOffline", err)
	}
	if assetCalls.Load() != 0 {
		t.Fatalf("asset calls = %d, want 0", assetCalls.Load())
	}
}

func TestLocalClientAcceptsOnlyExactLoopbackOrigins(t *testing.T) {
	valid := []string{"http://127.0.0.1:38421", "http://127.0.0.1:38421/", "http://[::1]:38421", "http://[::1]:38421/"}
	for _, endpoint := range valid {
		if _, err := NewLocalClient(Credential{Endpoint: endpoint, Token: localTestToken}, nil); err != nil {
			t.Fatalf("NewLocalClient(%q) error = %v", endpoint, err)
		}
	}

	invalid := []string{
		"https://127.0.0.1:38421",
		"http://localhost:38421",
		"http://127.0.0.2:38421",
		"http://127.0.0.1",
		"http://127.0.0.1:0",
		"http://127.0.0.1:038421",
		"http://127.0.0.1:65536",
		"http://user@127.0.0.1:38421",
		"http://127.0.0.1:38421/v1/local/context",
		"http://127.0.0.1:38421?token=secret",
		"http://127.0.0.1:38421#",
		"http://127.0.0.1:38421#fragment",
		"http://127.0.0.1:38421//",
	}
	for _, endpoint := range invalid {
		if _, err := NewLocalClient(Credential{Endpoint: endpoint, Token: localTestToken}, nil); err == nil {
			t.Fatalf("NewLocalClient(%q) unexpectedly succeeded", endpoint)
		}
	}
}

func TestLocalClientRefusesRedirectsAndInheritedProxy(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	defer redirect.Close()

	client, err := NewLocalClient(Credential{Endpoint: redirect.URL, Token: localTestToken}, redirect.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Read(context.Background()); !errors.Is(err, ErrInvalidLocalResponse) {
		t.Fatalf("redirect Read() error = %v, want ErrInvalidLocalResponse", err)
	}
	if redirected.Load() != 0 {
		t.Fatalf("redirect target requests = %d, want 0", redirected.Load())
	}

	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		proxyCalls.Add(1)
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertLocalRequest(t, r)
		writeLocalJSON(t, w, http.StatusOK, localTestManifest("direct"))
	}))
	defer direct.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	inherited := &http.Client{Transport: transport}
	client, err = NewLocalClient(Credential{Endpoint: direct.URL, Token: localTestToken}, inherited)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Read(context.Background()); err != nil {
		t.Fatalf("direct Read() error = %v", err)
	}
	if proxyCalls.Load() != 0 {
		t.Fatalf("inherited proxy calls = %d, want 0", proxyCalls.Load())
	}
}

func TestLocalClientIgnoresInjectedDialRoutingHooks(t *testing.T) {
	var attackerCalls atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerCalls.Add(1)
		writeLocalJSON(t, w, http.StatusOK, localTestManifest("rerouted"))
	}))
	defer attacker.Close()
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertLocalRequest(t, r)
		writeLocalJSON(t, w, http.StatusOK, localTestManifest("direct"))
	}))
	defer direct.Close()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, attacker.Listener.Addr().String())
	}
	client, err := NewLocalClient(Credential{Endpoint: direct.URL, Token: localTestToken}, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	contextValue, err := client.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if contextValue.Manifest.Text != "direct" || attackerCalls.Load() != 0 {
		t.Fatalf("text/attacker calls = %q/%d, want direct/0", contextValue.Manifest.Text, attackerCalls.Load())
	}
}

func TestLocalClientBoundsAndStrictlyDecodesManifest(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"protocol_version":1,"extra":true}`},
		{name: "trailing JSON", body: `{}` + "\n{}"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertLocalRequest(t, r)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client, err := NewLocalClient(Credential{Endpoint: server.URL, Token: localTestToken}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Read(context.Background()); !errors.Is(err, ErrInvalidLocalResponse) {
				t.Fatalf("Read() error = %v, want ErrInvalidLocalResponse", err)
			}
		})
	}
}

func TestDecodeLocalManifestRejectsDeclaredBodyOverLimitWithoutReading(t *testing.T) {
	body := &countingManifestBody{remaining: int64(maxLocalManifestBytes) + 1}
	response := &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          body,
		ContentLength: int64(maxLocalManifestBytes) + 1,
	}

	if _, err := decodeLocalManifest(response); !errors.Is(err, ErrInvalidLocalResponse) {
		t.Fatalf("decodeLocalManifest() error = %v, want ErrInvalidLocalResponse", err)
	}
	if body.bytesRead != 0 {
		t.Fatalf("declared oversized body bytes read = %d, want 0", body.bytesRead)
	}
}

func TestDecodeLocalManifestRejectsChunkedBodyAtReadLimit(t *testing.T) {
	body := &countingManifestBody{remaining: int64(maxLocalManifestBytes) + 1<<20}
	response := &http.Response{
		StatusCode:       http.StatusOK,
		Header:           http.Header{"Content-Type": []string{"application/json"}},
		Body:             body,
		ContentLength:    -1,
		TransferEncoding: []string{"chunked"},
	}

	if _, err := decodeLocalManifest(response); !errors.Is(err, ErrInvalidLocalResponse) {
		t.Fatalf("decodeLocalManifest() error = %v, want ErrInvalidLocalResponse", err)
	}
	if want := int64(maxLocalManifestBytes) + 1; body.bytesRead != want {
		t.Fatalf("chunked oversized body bytes read = %d, want bounded %d", body.bytesRead, want)
	}
}

func TestLocalClientMapsNoContextAndAppAbsenceToFixedErrors(t *testing.T) {
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertLocalRequest(t, r)
		http.NotFound(w, r)
	}))
	client, err := NewLocalClient(Credential{Endpoint: notFound.URL, Token: localTestToken}, notFound.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Read(context.Background()); !errors.Is(err, ErrNoContext) {
		t.Fatalf("404 Read() error = %v, want ErrNoContext", err)
	}
	notFound.Close()
	if _, err := client.Read(context.Background()); !errors.Is(err, ErrLocalUnavailable) {
		t.Fatalf("absent Read() error = %v, want ErrLocalUnavailable", err)
	}
}

func TestLocalClientVerifiesAssetStatusMIMELengthAndDigests(t *testing.T) {
	body := []byte("verified-image")
	base := localTestManifest("context", body)
	tests := []struct {
		name   string
		status int
		mime   string
		length int
		header string
		data   []byte
	}{
		{name: "status", status: http.StatusNotFound, mime: "image/png", length: len(body), header: base.Assets[0].Digest, data: body},
		{name: "MIME", status: http.StatusOK, mime: "image/jpeg", length: len(body), header: base.Assets[0].Digest, data: body},
		{name: "declared length", status: http.StatusOK, mime: "image/png", length: len(body) + 1, header: base.Assets[0].Digest, data: body},
		{name: "digest header", status: http.StatusOK, mime: "image/png", length: len(body), header: strings.Repeat("0", 64), data: body},
		{name: "body digest", status: http.StatusOK, mime: "image/png", length: len("wrong-image"), header: base.Assets[0].Digest, data: []byte("wrong-image")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertLocalRequest(t, r)
				if r.URL.Path == "/v1/local/context" {
					writeLocalJSON(t, w, http.StatusOK, base)
					return
				}
				w.Header().Set("Content-Type", test.mime)
				w.Header().Set("Content-Length", strconv.Itoa(test.length))
				w.Header().Set("X-MCPaste-SHA256", test.header)
				w.WriteHeader(test.status)
				_, _ = w.Write(test.data)
			}))
			defer server.Close()
			client, err := NewLocalClient(Credential{Endpoint: server.URL, Token: localTestToken}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Read(context.Background()); !errors.Is(err, ErrInvalidLocalResponse) {
				t.Fatalf("Read() error = %v, want ErrInvalidLocalResponse", err)
			}
		})
	}
}

func TestLocalClientBoundsTheCompleteReadCall(t *testing.T) {
	first := []byte("first-slow-image")
	second := []byte("second-slow-image")
	manifest := localTestManifest("context", first, second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertLocalRequest(t, r)
		if r.URL.Path == "/v1/local/context" {
			writeLocalJSON(t, w, http.StatusOK, manifest)
			return
		}
		select {
		case <-time.After(175 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		index := 0
		if strings.HasSuffix(r.URL.Path, "/1") {
			index = 1
		}
		writeLocalAsset(w, manifest.Assets[index], [][]byte{first, second}[index])
	}))
	defer server.Close()

	client, err := NewLocalClient(Credential{Endpoint: server.URL, Token: localTestToken}, &http.Client{
		Transport: server.Client().Transport,
		Timeout:   250 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = client.Read(context.Background())
	elapsed := time.Since(started)
	if !errors.Is(err, ErrLocalUnavailable) {
		t.Fatalf("Read() error = %v, want ErrLocalUnavailable", err)
	}
	if elapsed > 400*time.Millisecond {
		t.Fatalf("Read() elapsed = %v, want one bounded call", elapsed)
	}
}

func localTestManifest(text string, assets ...[]byte) peer.LocalContextResponse {
	manifest := peer.LocalContextResponse{
		ContextManifest: peer.ContextManifest{
			ProtocolVersion: peer.ProtocolVersion,
			Revision: peer.Revision{
				WallMillis: 1_755_500_000_000,
				Logical:    2,
				DeviceID:   "11111111-1111-4111-8111-111111111111",
			},
			SourceDeviceID: "11111111-1111-4111-8111-111111111111",
			UpdatedAt:      time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
			Text:           text,
			Assets:         make([]peer.AssetManifest, len(assets)),
		},
		SourceReachable: true,
		SyncState:       peer.SyncUpToDate,
	}
	for index, data := range assets {
		mime := "image/png"
		if index%2 == 1 {
			mime = "image/jpeg"
		}
		manifest.Assets[index] = peer.AssetManifest{
			Digest:   localDigest(data),
			MIMEType: mime,
			Width:    index + 1,
			Height:   index + 2,
			ByteSize: len(data),
		}
	}
	return manifest
}

func localDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func assertLocalRequest(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", r.Method)
	}
	if r.Header.Get("Authorization") != "Bearer "+localTestToken {
		t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
	}
	if r.URL.RawQuery != "" || strings.Contains(r.URL.String(), localTestToken) {
		t.Errorf("request URL contains query or token: %q", r.URL.String())
	}
}

func writeLocalJSON(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func writeLocalAsset(w http.ResponseWriter, manifest peer.AssetManifest, data []byte) {
	w.Header().Set("Content-Type", manifest.MIMEType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("X-MCPaste-SHA256", manifest.Digest)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

type countingManifestBody struct {
	remaining int64
	bytesRead int64
}

func (b *countingManifestBody) Read(buffer []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(buffer)) > b.remaining {
		buffer = buffer[:b.remaining]
	}
	for index := range buffer {
		buffer[index] = 'x'
	}
	b.remaining -= int64(len(buffer))
	b.bytesRead += int64(len(buffer))
	return len(buffer), nil
}

func (*countingManifestBody) Close() error { return nil }
