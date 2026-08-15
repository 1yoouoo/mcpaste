package images

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/1yoouoo/mcpaste/internal/secure"
)

// S3Config addresses an S3-compatible bucket. Prefix scopes every key this store
// touches, so a bucket shared with other projects stays untouched.
type S3Config struct {
	Endpoint  string
	Region    string
	Bucket    string
	Prefix    string
	AccessKey string
	SecretKey string
}

// S3Store keeps encrypted assets in an S3-compatible bucket. Bodies are ciphertext,
// so the bucket never holds readable paste content.
type S3Store struct {
	cfg          S3Config
	keyring      *secure.Keyring
	client       *http.Client
	now          func() time.Time
	bucketInHost bool
}

const s3RequestTimeout = 30 * time.Second

func NewS3Store(cfg S3Config, keyring *secure.Keyring, client *http.Client) (*S3Store, error) {
	if keyring == nil {
		return nil, errors.New("image object storage requires an encryption keyring")
	}
	if cfg.Region == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("image object storage requires endpoint, region, bucket, and credentials")
	}
	endpoint, err := url.Parse(strings.TrimSuffix(cfg.Endpoint, "/"))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") {
		return nil, errors.New("image object storage endpoint must be an absolute http or https URL")
	}
	if strings.ContainsAny(cfg.Bucket, "/?#") {
		return nil, errors.New("image object storage bucket name is invalid")
	}
	cfg.Endpoint = endpoint.String()
	cfg.Prefix = normalizeS3Prefix(cfg.Prefix)
	if client == nil {
		client = &http.Client{Timeout: s3RequestTimeout}
	}
	return &S3Store{
		cfg:          cfg,
		keyring:      keyring,
		client:       client,
		now:          time.Now,
		bucketInHost: strings.HasPrefix(endpoint.Hostname(), cfg.Bucket+"."),
	}, nil
}

func normalizeS3Prefix(prefix string) string {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return ""
	}
	return prefix + "/"
}

func (s *S3Store) Put(workspaceID, pasteID, revisionID string, index int, plaintext []byte) (StoredAsset, error) {
	if err := validateIdentifiers(workspaceID, pasteID, revisionID, index); err != nil {
		return StoredAsset{}, ErrInvalidImage
	}
	objectID := fmt.Sprintf("%s:%s:%s:%d", workspaceID, pasteID, revisionID, index)
	envelope, err := s.keyring.Encrypt("paste-image", objectID, plaintext)
	if err != nil {
		return StoredAsset{}, errors.New("encrypt image")
	}
	storageKey := fmt.Sprintf("%s/%s/%s/asset-%02d.bin", workspaceID, pasteID, revisionID, index)
	response, err := s.send(http.MethodPut, s.cfg.Prefix+storageKey, nil, envelope.Ciphertext)
	if err != nil {
		return StoredAsset{}, errors.New("write image")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return StoredAsset{}, errors.New("write image")
	}
	return StoredAsset{StorageKey: storageKey, Envelope: envelope}, nil
}

func (s *S3Store) Open(asset StoredAsset) ([]byte, error) {
	if !validStorageKey(asset.StorageKey) {
		return nil, ErrInvalidImage
	}
	response, err := s.send(http.MethodGet, s.cfg.Prefix+asset.StorageKey, nil, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrUnavailable
	}
	ciphertext, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, ErrUnavailable
	}
	plain, err := s.keyring.Decrypt("paste-image", objectIDFromKey(asset.StorageKey), secure.Envelope{
		KeyID:      asset.Envelope.KeyID,
		Nonce:      asset.Envelope.Nonce,
		Ciphertext: ciphertext,
	})
	if err != nil {
		return nil, ErrUnavailable
	}
	return plain, nil
}

func (s *S3Store) Remove(asset StoredAsset) error {
	if !validStorageKey(asset.StorageKey) {
		return ErrInvalidImage
	}
	return s.deleteObject(s.cfg.Prefix + asset.StorageKey)
}

func (s *S3Store) RemoveTree(workspaceID, pasteID, revisionID string) error {
	if err := validateIdentifiers(workspaceID, pasteID, revisionID, 0); err != nil {
		return err
	}
	return s.removePrefix(fmt.Sprintf("%s%s/%s/%s/", s.cfg.Prefix, workspaceID, pasteID, revisionID))
}

func (s *S3Store) RemovePaste(workspaceID, pasteID string) error {
	if !validUUID(workspaceID) || !validUUID(pasteID) {
		return ErrInvalidImage
	}
	return s.removePrefix(fmt.Sprintf("%s%s/%s/", s.cfg.Prefix, workspaceID, pasteID))
}

// removePrefix deletes every object under prefix. Listed keys are re-checked against
// the prefix so a surprising listing can never delete a neighbouring project's objects.
func (s *S3Store) removePrefix(prefix string) error {
	token := ""
	for {
		keys, next, err := s.listObjects(prefix, token)
		if err != nil {
			return err
		}
		for _, key := range keys {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			if err := s.deleteObject(key); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		token = next
	}
}

func (s *S3Store) listObjects(prefix, continuationToken string) ([]string, string, error) {
	query := url.Values{}
	query.Set("list-type", "2")
	query.Set("prefix", prefix)
	if continuationToken != "" {
		query.Set("continuation-token", continuationToken)
	}
	response, err := s.send(http.MethodGet, "", query, nil)
	if err != nil {
		return nil, "", errors.New("list images")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", errors.New("list images")
	}
	var payload struct {
		XMLName               xml.Name `xml:"ListBucketResult"`
		IsTruncated           bool     `xml:"IsTruncated"`
		NextContinuationToken string   `xml:"NextContinuationToken"`
		Contents              []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	if err := xml.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, "", errors.New("list images")
	}
	keys := make([]string, 0, len(payload.Contents))
	for _, item := range payload.Contents {
		keys = append(keys, item.Key)
	}
	if !payload.IsTruncated {
		return keys, "", nil
	}
	return keys, payload.NextContinuationToken, nil
}

func (s *S3Store) deleteObject(key string) error {
	response, err := s.send(http.MethodDelete, key, nil, nil)
	if err != nil {
		return errors.New("remove image")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent &&
		response.StatusCode != http.StatusOK &&
		response.StatusCode != http.StatusNotFound {
		return errors.New("remove image")
	}
	return nil
}

// objectPath renders the request path for a key. A bucket-scoped endpoint
// (https://bucket.region.example.com) carries the bucket in the host, so the path holds
// only the key; a regional endpoint addresses the bucket through the path instead.
func (s *S3Store) objectPath(key string) string {
	path := "/"
	if !s.bucketInHost {
		path += s.cfg.Bucket + "/"
	}
	return path + key
}

func (s *S3Store) send(method, key string, query url.Values, body []byte) (*http.Response, error) {
	path := strings.TrimSuffix(s.objectPath(key), "/")
	if path == "" {
		path = "/"
	}
	target := s.cfg.Endpoint + encodeS3Path(path)
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	ctx, cancel := context.WithTimeout(context.Background(), s3RequestTimeout)
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	request.ContentLength = int64(len(body))
	s.sign(request, path, query, body)
	response, err := s.client.Do(request)
	if err != nil {
		cancel()
		return nil, err
	}
	response.Body = cancelOnClose{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// sign applies AWS Signature Version 4 for the s3 service.
func (s *S3Store) sign(request *http.Request, canonicalPath string, query url.Values, body []byte) {
	digest := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(digest[:])
	now := s.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	request.Header.Set("X-Amz-Content-Sha256", payloadHash)
	request.Header.Set("X-Amz-Date", amzDate)

	canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n", request.URL.Host, payloadHash, amzDate)
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		request.Method,
		encodeS3Path(canonicalPath),
		canonicalQuery(query),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, s.cfg.Region, "s3", "aws4_request"}, "/")
	requestDigest := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(requestDigest[:]),
	}, "\n")

	signingKey := hmacSHA256([]byte("AWS4"+s.cfg.SecretKey), dateStamp)
	signingKey = hmacSHA256(signingKey, s.cfg.Region)
	signingKey = hmacSHA256(signingKey, "s3")
	signingKey = hmacSHA256(signingKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	request.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.cfg.AccessKey, scope, signedHeaders, signature,
	))
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(value))
	return mac.Sum(nil)
}

// canonicalQuery renders query parameters the way SigV4 expects: sorted, with each
// name and value URI-encoded and spaces as %20.
func canonicalQuery(query url.Values) string {
	if len(query) == 0 {
		return ""
	}
	names := make([]string, 0, len(query))
	for name := range query {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		values := append([]string(nil), query[name]...)
		sort.Strings(values)
		for _, value := range values {
			parts = append(parts, encodeS3Component(name)+"="+encodeS3Component(value))
		}
	}
	return strings.Join(parts, "&")
}

func encodeS3Path(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = encodeS3Component(segment)
	}
	return strings.Join(segments, "/")
}

// encodeS3Component percent-encodes everything outside the RFC 3986 unreserved set.
func encodeS3Component(value string) string {
	var builder strings.Builder
	for i := 0; i < len(value); i++ {
		character := value[i]
		switch {
		case character >= 'A' && character <= 'Z',
			character >= 'a' && character <= 'z',
			character >= '0' && character <= '9',
			character == '-', character == '_', character == '.', character == '~':
			builder.WriteByte(character)
		default:
			builder.WriteString(fmt.Sprintf("%%%02X", character))
		}
	}
	return builder.String()
}

// validStorageKey accepts only the workspace/paste/revision/asset layout this store writes.
func validStorageKey(key string) bool {
	segments := strings.Split(key, "/")
	if len(segments) != 4 {
		return false
	}
	if !validUUID(segments[0]) || !validUUID(segments[1]) || !validUUID(segments[2]) {
		return false
	}
	return objectIDFromKey(key) != "invalid"
}
