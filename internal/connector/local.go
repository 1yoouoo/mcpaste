package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/1yoouoo/mcpaste/internal/peer"
)

const (
	maxLocalCredentialBytes = 16 << 10
	maxLocalTokenBytes      = 4 << 10
	maxLocalManifestBytes   = peer.MaxContextJSONBytes
	localRequestTimeout     = 10 * time.Second
)

var (
	ErrInvalidCredential    = errors.New("invalid connector credential")
	ErrLocalUnavailable     = errors.New("local MCPaste app unavailable")
	ErrNoContext            = errors.New("MCPaste context unavailable")
	ErrSourceOffline        = errors.New("MCPaste context source offline")
	ErrInvalidLocalResponse = errors.New("invalid local MCPaste response")
)

type LocalContext struct {
	Manifest peer.ContextManifest
	Assets   [][]byte
}

type LocalClient struct {
	baseURL     string
	token       string
	client      *http.Client
	readTimeout time.Duration
}

func NewLocalClient(credential Credential, client *http.Client) (*LocalClient, error) {
	baseURL, err := validateLocalCredential(credential)
	if err != nil {
		return nil, err
	}
	httpClient, err := localHTTPClient(client)
	if err != nil {
		return nil, err
	}
	return &LocalClient{baseURL: baseURL, token: credential.Token, client: httpClient, readTimeout: httpClient.Timeout}, nil
}

func (c *LocalClient) Read(ctx context.Context) (LocalContext, error) {
	if c == nil || c.client == nil || ctx == nil || c.readTimeout <= 0 {
		return LocalContext{}, ErrLocalUnavailable
	}
	readContext, cancel := context.WithTimeout(ctx, c.readTimeout)
	defer cancel()
	request, err := c.request(readContext, "/v1/local/context")
	if err != nil {
		return LocalContext{}, ErrInvalidLocalResponse
	}
	response, err := c.client.Do(request)
	if err != nil {
		return LocalContext{}, ErrLocalUnavailable
	}
	manifest, err := decodeLocalManifest(response)
	if err != nil {
		return LocalContext{}, err
	}
	if !manifest.SourceReachable {
		return LocalContext{}, ErrSourceOffline
	}
	if !validLocalManifest(manifest) {
		return LocalContext{}, ErrInvalidLocalResponse
	}

	assets := make([][]byte, len(manifest.Assets))
	for index, asset := range manifest.Assets {
		data, err := c.readAsset(readContext, index, asset)
		if err != nil {
			return LocalContext{}, err
		}
		assets[index] = data
	}
	return LocalContext{Manifest: manifest.ContextManifest, Assets: assets}, nil
}

func (c *LocalClient) readAsset(ctx context.Context, index int, manifest peer.AssetManifest) ([]byte, error) {
	request, err := c.request(ctx, "/v1/local/context/assets/"+strconv.Itoa(index))
	if err != nil {
		return nil, ErrInvalidLocalResponse
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, ErrLocalUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != manifest.MIMEType ||
		response.ContentLength != int64(manifest.ByteSize) || response.Header.Get("X-MCPaste-SHA256") != manifest.Digest {
		return nil, ErrInvalidLocalResponse
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(manifest.ByteSize)+1))
	if err != nil || len(data) != manifest.ByteSize {
		return nil, ErrInvalidLocalResponse
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != manifest.Digest {
		return nil, ErrInvalidLocalResponse
	}
	return data, nil
}

func (c *LocalClient) request(ctx context.Context, path string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, ErrInvalidLocalResponse
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	return request, nil
}

func validateLocalCredential(credential Credential) (string, error) {
	parsed, err := url.Parse(credential.Endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Opaque != "" || parsed.Host == "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" || parsed.RawFragment != "" || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") {
		return "", ErrInvalidCredential
	}
	port, err := strconv.ParseUint(parsed.Port(), 10, 16)
	if err != nil || port == 0 || parsed.Port() != strconv.FormatUint(port, 10) {
		return "", ErrInvalidCredential
	}
	origin := "http://" + net.JoinHostPort(parsed.Hostname(), parsed.Port())
	if credential.Endpoint != origin && credential.Endpoint != origin+"/" {
		return "", ErrInvalidCredential
	}
	if credential.Token == "" || len(credential.Token) > maxLocalTokenBytes || strings.IndexFunc(credential.Token, func(character rune) bool {
		return unicode.IsControl(character) || unicode.IsSpace(character)
	}) != -1 {
		return "", ErrInvalidCredential
	}
	return strings.TrimSuffix(credential.Endpoint, "/"), nil
}

func localHTTPClient(client *http.Client) (*http.Client, error) {
	if client == nil {
		client = http.DefaultClient
	}
	copyClient := *client
	switch copyClient.Transport.(type) {
	case nil:
	case *http.Transport:
	default:
		return nil, ErrInvalidCredential
	}
	dialer := &net.Dialer{Timeout: localRequestTimeout, KeepAlive: 30 * time.Second}
	copyClient.Transport = &http.Transport{
		DialContext:            dialer.DialContext,
		DisableCompression:     true,
		MaxIdleConns:           1,
		MaxIdleConnsPerHost:    1,
		IdleConnTimeout:        30 * time.Second,
		ResponseHeaderTimeout:  localRequestTimeout,
		MaxResponseHeaderBytes: 64 << 10,
	}
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copyClient.Timeout <= 0 || copyClient.Timeout > localRequestTimeout {
		copyClient.Timeout = localRequestTimeout
	}
	return &copyClient, nil
}

func decodeLocalManifest(response *http.Response) (peer.LocalContextResponse, error) {
	if response == nil || response.Body == nil {
		return peer.LocalContextResponse{}, ErrInvalidLocalResponse
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return peer.LocalContextResponse{}, ErrNoContext
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || err != nil || mediaType != "application/json" || response.ContentLength > maxLocalManifestBytes {
		return peer.LocalContextResponse{}, ErrInvalidLocalResponse
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxLocalManifestBytes+1))
	if err != nil || len(body) > maxLocalManifestBytes {
		return peer.LocalContextResponse{}, ErrInvalidLocalResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var manifest peer.LocalContextResponse
	if err := decoder.Decode(&manifest); err != nil {
		return peer.LocalContextResponse{}, ErrInvalidLocalResponse
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return peer.LocalContextResponse{}, ErrInvalidLocalResponse
	}
	return manifest, nil
}

func validLocalManifest(manifest peer.LocalContextResponse) bool {
	if manifest.ProtocolVersion != peer.ProtocolVersion || manifest.Revision.DeviceID == "" ||
		manifest.SourceDeviceID == "" || manifest.Revision.DeviceID != manifest.SourceDeviceID ||
		len(manifest.Text) > peer.MaxTextBytes || len(manifest.Assets) > peer.MaxAssets {
		return false
	}
	switch manifest.SyncState {
	case peer.SyncUpToDate, peer.SyncUpdating, peer.SyncWaiting:
	default:
		return false
	}
	bundleBytes := len(manifest.Text)
	for _, asset := range manifest.Assets {
		if !validLocalAssetManifest(asset) || bundleBytes > peer.MaxBundleBytes-asset.ByteSize {
			return false
		}
		bundleBytes += asset.ByteSize
	}
	return true
}

func validLocalAssetManifest(asset peer.AssetManifest) bool {
	if (asset.MIMEType != "image/png" && asset.MIMEType != "image/jpeg") || asset.Width <= 0 || asset.Height <= 0 ||
		asset.ByteSize < 0 || asset.ByteSize > peer.MaxAssetBytes || len(asset.Digest) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(asset.Digest)
	return err == nil && strings.ToLower(asset.Digest) == asset.Digest
}
