package images

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/1yoouoo/mcpaste/internal/secure"
)

const (
	s3TestWorkspace = "00000000-0000-4000-8000-000000000001"
	s3TestPaste     = "00000000-0000-4000-8000-000000000002"
	s3TestRevision  = "00000000-0000-4000-8000-000000000003"
)

type recordedRequest struct {
	method string
	path   string
	query  string
	header http.Header
	body   []byte
}

type s3Recorder struct {
	mu       sync.Mutex
	requests []recordedRequest
	objects  map[string][]byte
	listKeys []string
}

func newS3Recorder() *s3Recorder {
	return &s3Recorder{objects: map[string][]byte{}}
}

func (r *s3Recorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body := make([]byte, 0)
		if req.Body != nil {
			buffer := new(bytes.Buffer)
			_, _ = buffer.ReadFrom(req.Body)
			body = buffer.Bytes()
		}
		r.mu.Lock()
		r.requests = append(r.requests, recordedRequest{
			method: req.Method,
			path:   req.URL.Path,
			query:  req.URL.RawQuery,
			header: req.Header.Clone(),
			body:   body,
		})
		r.mu.Unlock()

		if req.URL.Query().Get("list-type") == "2" {
			type object struct {
				Key string `xml:"Key"`
			}
			payload := struct {
				XMLName  xml.Name `xml:"ListBucketResult"`
				Contents []object `xml:"Contents"`
			}{}
			for _, key := range r.listKeys {
				payload.Contents = append(payload.Contents, object{Key: key})
			}
			w.Header().Set("Content-Type", "application/xml")
			_ = xml.NewEncoder(w).Encode(payload)
			return
		}

		switch req.Method {
		case http.MethodPut:
			r.mu.Lock()
			r.objects[req.URL.Path] = body
			r.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			r.mu.Lock()
			stored, ok := r.objects[req.URL.Path]
			r.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(stored)
		case http.MethodDelete:
			r.mu.Lock()
			delete(r.objects, req.URL.Path)
			r.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func (r *s3Recorder) snapshot() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedRequest(nil), r.requests...)
}

func newTestS3Store(t *testing.T, endpoint, prefix string) *S3Store {
	t.Helper()
	keyring, err := secure.NewKeyring(
		"image-test",
		map[string][]byte{"image-test": bytes.Repeat([]byte{0x42}, 32)},
		&storageBytes{value: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewS3Store(S3Config{
		Endpoint:  endpoint,
		Region:    "sgp1",
		Bucket:    "mcpaste",
		Prefix:    prefix,
		AccessKey: "DO00TESTACCESSKEY000",
		SecretKey: "test-secret-key-value-0000000000000000000",
	}, keyring, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestS3StoreUploadsCiphertextUnderPrefixedKey(t *testing.T) {
	recorder := newS3Recorder()
	server := httptest.NewServer(recorder.handler())
	defer server.Close()
	store := newTestS3Store(t, server.URL, "prod/")

	plaintext := []byte("image bytes")
	asset, err := store.Put(s3TestWorkspace, s3TestPaste, s3TestRevision, 0, plaintext)
	if err != nil {
		t.Fatal(err)
	}

	wantKey := fmt.Sprintf("%s/%s/%s/asset-00.bin", s3TestWorkspace, s3TestPaste, s3TestRevision)
	if asset.StorageKey != wantKey {
		t.Fatalf("storage key = %q, want %q", asset.StorageKey, wantKey)
	}
	requests := recorder.snapshot()
	if len(requests) != 1 || requests[0].method != http.MethodPut {
		t.Fatalf("requests = %+v, want a single PUT", requests)
	}
	if got, want := requests[0].path, "/mcpaste/prod/"+wantKey; got != want {
		t.Fatalf("request path = %q, want %q", got, want)
	}
	if bytes.Equal(requests[0].body, plaintext) {
		t.Fatal("uploaded body must be ciphertext, not plaintext")
	}
	if !bytes.Equal(requests[0].body, asset.Envelope.Ciphertext) {
		t.Fatal("uploaded body must match the envelope ciphertext")
	}
	digest := sha256.Sum256(requests[0].body)
	if got, want := requests[0].header.Get("X-Amz-Content-Sha256"), hex.EncodeToString(digest[:]); got != want {
		t.Fatalf("content sha256 = %q, want %q", got, want)
	}
	authorization := requests[0].header.Get("Authorization")
	if !strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 Credential=DO00TESTACCESSKEY000/") {
		t.Fatalf("authorization = %q", authorization)
	}
	if !strings.Contains(authorization, "/sgp1/s3/aws4_request") ||
		!strings.Contains(authorization, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		t.Fatalf("authorization = %q", authorization)
	}
	if requests[0].header.Get("X-Amz-Date") == "" {
		t.Fatal("missing x-amz-date header")
	}
}

func TestS3StoreRoundTripsThroughEncryption(t *testing.T) {
	recorder := newS3Recorder()
	server := httptest.NewServer(recorder.handler())
	defer server.Close()
	store := newTestS3Store(t, server.URL, "")

	plaintext := []byte("round trip bytes")
	asset, err := store.Put(s3TestWorkspace, s3TestPaste, s3TestRevision, 3, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(asset)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, plaintext) {
		t.Fatalf("restored = %q, want %q", restored, plaintext)
	}
}

func TestS3StoreOpenReportsUnavailableWhenObjectMissing(t *testing.T) {
	recorder := newS3Recorder()
	server := httptest.NewServer(recorder.handler())
	defer server.Close()
	store := newTestS3Store(t, server.URL, "")

	_, err := store.Open(StoredAsset{StorageKey: fmt.Sprintf("%s/%s/%s/asset-00.bin", s3TestWorkspace, s3TestPaste, s3TestRevision)})
	if err != ErrUnavailable {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestS3StoreRemoveTreeDeletesOnlyListedRevisionKeys(t *testing.T) {
	recorder := newS3Recorder()
	// S3 lists object keys without the bucket; the request path adds it back for path-style addressing.
	objectPrefix := fmt.Sprintf("prod/%s/%s/%s/", s3TestWorkspace, s3TestPaste, s3TestRevision)
	recorder.listKeys = []string{objectPrefix + "asset-00.bin", objectPrefix + "asset-01.bin"}
	server := httptest.NewServer(recorder.handler())
	defer server.Close()
	store := newTestS3Store(t, server.URL, "prod/")

	if err := store.RemoveTree(s3TestWorkspace, s3TestPaste, s3TestRevision); err != nil {
		t.Fatal(err)
	}

	var listed, deleted []string
	for _, request := range recorder.snapshot() {
		switch {
		case strings.Contains(request.query, "list-type=2"):
			listed = append(listed, request.query)
		case request.method == http.MethodDelete:
			deleted = append(deleted, request.path)
		}
	}
	if len(listed) != 1 || !strings.Contains(listed[0], "prefix=prod%2F"+s3TestWorkspace) {
		t.Fatalf("list queries = %v", listed)
	}
	requestPrefix := "/mcpaste/" + objectPrefix
	if len(deleted) != 2 || deleted[0] != requestPrefix+"asset-00.bin" || deleted[1] != requestPrefix+"asset-01.bin" {
		t.Fatalf("deleted = %v", deleted)
	}
}

func TestS3StoreRemoveTreeIgnoresKeysOutsideItsPrefix(t *testing.T) {
	recorder := newS3Recorder()
	objectPrefix := fmt.Sprintf("prod/%s/%s/%s/", s3TestWorkspace, s3TestPaste, s3TestRevision)
	recorder.listKeys = []string{objectPrefix + "asset-00.bin", "assets/other-project/file.png"}
	server := httptest.NewServer(recorder.handler())
	defer server.Close()
	store := newTestS3Store(t, server.URL, "prod/")

	if err := store.RemoveTree(s3TestWorkspace, s3TestPaste, s3TestRevision); err != nil {
		t.Fatal(err)
	}

	for _, request := range recorder.snapshot() {
		if request.method == http.MethodDelete && strings.Contains(request.path, "other-project") {
			t.Fatalf("deleted a key outside the configured prefix: %s", request.path)
		}
	}
}

func TestS3StoreRejectsInvalidIdentifiers(t *testing.T) {
	recorder := newS3Recorder()
	server := httptest.NewServer(recorder.handler())
	defer server.Close()
	store := newTestS3Store(t, server.URL, "")

	if _, err := store.Put("not-a-uuid", s3TestPaste, s3TestRevision, 0, []byte("x")); err != ErrInvalidImage {
		t.Fatalf("err = %v, want ErrInvalidImage", err)
	}
	if err := store.RemovePaste("not-a-uuid", s3TestPaste); err != ErrInvalidImage {
		t.Fatalf("err = %v, want ErrInvalidImage", err)
	}
	if _, err := store.Open(StoredAsset{StorageKey: "../escape/asset-00.bin"}); err != ErrInvalidImage {
		t.Fatalf("err = %v, want ErrInvalidImage", err)
	}
	if len(recorder.snapshot()) != 0 {
		t.Fatal("invalid identifiers must not reach the network")
	}
}

func TestNewS3StoreRequiresCompleteConfiguration(t *testing.T) {
	keyring, err := secure.NewKeyring(
		"image-test",
		map[string][]byte{"image-test": bytes.Repeat([]byte{0x42}, 32)},
		&storageBytes{value: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	complete := S3Config{
		Endpoint:  "https://sgp1.digitaloceanspaces.com",
		Region:    "sgp1",
		Bucket:    "mcpaste",
		AccessKey: "key",
		SecretKey: "secret",
	}
	for name, mutate := range map[string]func(*S3Config){
		"endpoint":  func(c *S3Config) { c.Endpoint = "" },
		"region":    func(c *S3Config) { c.Region = "" },
		"bucket":    func(c *S3Config) { c.Bucket = "" },
		"accessKey": func(c *S3Config) { c.AccessKey = "" },
		"secretKey": func(c *S3Config) { c.SecretKey = "" },
		"scheme":    func(c *S3Config) { c.Endpoint = "ftp://sgp1.example.com" },
	} {
		cfg := complete
		mutate(&cfg)
		if _, err := NewS3Store(cfg, keyring, nil); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
	if _, err := NewS3Store(complete, nil, nil); err == nil {
		t.Fatal("expected an error without a keyring")
	}
}
