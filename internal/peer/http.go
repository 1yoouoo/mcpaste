package peer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxAnnounceBodyBytes   = 4 << 10
	maxLocalContextBody    = MaxContextJSONBytes
	localContextRoute      = "/v1/local/context"
	peerContextRoute       = "/v1/context"
	localContextAssetsBase = "/v1/local/context/assets/"
	peerContextAssetsBase  = "/v1/context/assets/"
	localAssetsBase        = "/v1/local/assets/"
)

var (
	errHTTPInvalidBody = errors.New("invalid HTTP request body")
	errHTTPBodyTooBig  = errors.New("HTTP request body too large")
	errHTTPOperation   = errors.New("HTTP operation failed")
)

// HandlerOptions supplies the in-memory peer runtime dependencies.
type HandlerOptions struct {
	Store          *Store
	Registry       *Registry
	LocalDevice    KnownPeer
	LocalToken     string
	AllowedPeers   *AllowedPeerIPs
	ReachablePeers *AllowedPeerIPs
	SyncState      func() SyncState
	Announce       func(context.Context, Revision) error
}

// AllowedPeerIPs is an immutable address snapshot swapped atomically under a
// mutex. Each published snapshot owns its map and is never mutated afterward.
type AllowedPeerIPs struct {
	mu    sync.RWMutex
	addrs map[netip.Addr]struct{}
}

func (a *AllowedPeerIPs) Replace(addresses []netip.Addr) {
	if a == nil {
		return
	}
	next := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		if !address.IsValid() || address.Zone() != "" {
			continue
		}
		next[address.Unmap()] = struct{}{}
	}
	a.mu.Lock()
	a.addrs = next
	a.mu.Unlock()
}

func (a *AllowedPeerIPs) Contains(address netip.Addr) bool {
	if a == nil || !address.IsValid() || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	a.mu.RLock()
	_, ok := a.addrs[address]
	a.mu.RUnlock()
	return ok
}

// NewHTTPServer returns the fixed-timeout server used by the peer runtime.
func NewHTTPServer(addr string, handler http.Handler) *http.Server {
	if handler == nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeHTTPError(w, http.StatusServiceUnavailable)
		})
	}
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
}

type httpHandler struct {
	options              HandlerOptions
	localTokenDigest     [sha256.Size]byte
	localTokenConfigured bool
	announceMu           sync.Mutex
	announceActive       *announceCall
}

type announceCall struct {
	revision     Revision
	publicDone   chan struct{}
	callbackDone chan struct{}
	resultOnce   sync.Once
	resultErr    error
	followers    int
}

func (call *announceCall) resolvePublicResult(err error) {
	call.resultOnce.Do(func() {
		call.resultErr = err
		close(call.publicDone)
	})
}

func NewHandler(options HandlerOptions) http.Handler {
	configured := options.LocalToken != ""
	digest := sha256.Sum256([]byte("Bearer " + options.LocalToken))
	options.LocalToken = ""
	return &httpHandler{
		options:              options,
		localTokenDigest:     digest,
		localTokenConfigured: configured,
	}
}

type httpRouteKind uint8

const (
	httpPeerRoute httpRouteKind = iota + 1
	httpLocalRoute
)

type httpRoute struct {
	kind        httpRouteKind
	method      string
	value       string
	index       int
	assetDigest string
}

func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, ok := matchHTTPRoute(r)
	if !ok {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	if h == nil {
		if route.kind == httpLocalRoute {
			writeHTTPError(w, http.StatusUnauthorized)
		} else {
			writeHTTPError(w, http.StatusForbidden)
		}
		return
	}
	if route.kind == httpLocalRoute {
		if !authorizedLocalRequest(r, h.localTokenDigest, h.localTokenConfigured) {
			writeHTTPError(w, http.StatusUnauthorized)
			return
		}
	} else if !authorizedPeerRequest(r, h.options.AllowedPeers) {
		writeHTTPError(w, http.StatusForbidden)
		return
	}
	if r.Method != route.method {
		writeHTTPError(w, http.StatusMethodNotAllowed)
		return
	}
	if !h.validConfiguration() {
		writeHTTPError(w, http.StatusServiceUnavailable)
		return
	}

	switch route.value {
	case "health":
		h.serveHealth(w)
	case "context":
		h.serveContext(w)
	case "context-asset":
		h.serveContextAsset(w, routeIndex(route))
	case "announce":
		h.serveAnnounce(w, r)
	case "local-asset":
		h.serveLocalAsset(w, r, route.assetDigest)
	case "local-context":
		if route.method == http.MethodGet {
			h.serveLocalContext(w)
		} else {
			h.serveLocalContextUpdate(w, r)
		}
	case "local-context-asset":
		h.serveLocalContextAsset(w, routeIndex(route))
	case "devices":
		h.serveDevices(w)
	default:
		writeHTTPError(w, http.StatusNotFound)
	}
}

func matchHTTPRoute(r *http.Request) (httpRoute, bool) {
	if r == nil || r.URL == nil {
		return httpRoute{}, false
	}
	switch r.URL.Path {
	case "/v1/health":
		kind := httpPeerRoute
		if address, valid := parseRemoteAddress(r.RemoteAddr); valid && address.IsLoopback() {
			kind = httpLocalRoute
		}
		return httpRoute{kind: kind, method: http.MethodGet, value: "health"}, true
	case peerContextRoute:
		return httpRoute{kind: httpPeerRoute, method: http.MethodGet, value: "context"}, true
	case "/v1/announce":
		return httpRoute{kind: httpPeerRoute, method: http.MethodPost, value: "announce"}, true
	case localContextRoute:
		if r.Method == http.MethodPut {
			return httpRoute{kind: httpLocalRoute, method: http.MethodPut, value: "local-context"}, true
		}
		return httpRoute{kind: httpLocalRoute, method: http.MethodGet, value: "local-context"}, true
	case "/v1/local/devices":
		return httpRoute{kind: httpLocalRoute, method: http.MethodGet, value: "devices"}, true
	}
	if index, ok := routeSuffix(r.URL.Path, peerContextAssetsBase, false); ok {
		parsed, _ := strconv.Atoi(index)
		return httpRoute{kind: httpPeerRoute, method: http.MethodGet, value: "context-asset", index: parsed}, true
	}
	if index, ok := routeSuffix(r.URL.Path, localContextAssetsBase, false); ok {
		parsed, _ := strconv.Atoi(index)
		return httpRoute{kind: httpLocalRoute, method: http.MethodGet, value: "local-context-asset", index: parsed}, true
	}
	if digest, ok := routeSuffix(r.URL.Path, localAssetsBase, true); ok {
		return httpRoute{kind: httpLocalRoute, method: http.MethodPut, value: "local-asset", assetDigest: digest}, true
	}
	return httpRoute{}, false
}

// routeIndex is kept separate from route.value's semantic name. The value is
// replaced by the parsed path suffix in matchHTTPRoute for asset routes.
func routeIndex(route httpRoute) int {
	return route.index
}

func routeSuffix(path, prefix string, digest bool) (string, bool) {
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	suffix := strings.TrimPrefix(path, prefix)
	if suffix == "" || strings.Contains(suffix, "/") {
		return "", false
	}
	if digest {
		return suffix, validLowerDigest(suffix)
	}
	if !allDecimal(suffix) {
		return "", false
	}
	index, err := strconv.ParseUint(suffix, 10, 0)
	if err != nil || uint64(int(index)) != index {
		return "", false
	}
	return suffix, true
}

func validLowerDigest(value string) bool {
	if len(value) != sha256DigestLength {
		return false
	}
	return allLowerHex(value)
}

const sha256DigestLength = 64

func allLowerHex(value string) bool {
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func allDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (h *httpHandler) validConfiguration() bool {
	if h == nil {
		return false
	}
	o := h.options
	return o.Store != nil && o.Registry != nil && o.AllowedPeers != nil && o.ReachablePeers != nil && o.SyncState != nil && o.Announce != nil &&
		o.LocalDevice.DeviceID != "" && o.LocalDevice.DisplayName != "" && validHTTPStore(o.Store)
}

func validHTTPStore(store *Store) bool {
	return store != nil && store.clock != nil && store.now != nil && store.localDeviceID != ""
}

func authorizedLocalRequest(r *http.Request, expected [sha256.Size]byte, configured bool) bool {
	if !configured || r == nil {
		return false
	}
	address, ok := parseRemoteAddress(r.RemoteAddr)
	if !ok || !address.IsLoopback() {
		return false
	}
	values := requestHeaderValues(r.Header, "Authorization")
	if len(values) != 1 {
		return false
	}
	supplied := sha256.Sum256([]byte(values[0]))
	return subtle.ConstantTimeCompare(expected[:], supplied[:]) == 1
}

func authorizedPeerRequest(r *http.Request, allowed *AllowedPeerIPs) bool {
	if r == nil || allowed == nil {
		return false
	}
	address, ok := parseRemoteAddress(r.RemoteAddr)
	return ok && !address.IsLoopback() && allowed.Contains(address)
}

func parseRemoteAddress(value string) (netip.Addr, bool) {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return netip.Addr{}, false
	}
	if parsedPort, err := strconv.ParseUint(port, 10, 16); err != nil || parsedPort > math.MaxUint16 {
		return netip.Addr{}, false
	}
	address, err := netip.ParseAddr(host)
	if err != nil || !address.IsValid() || address.Zone() != "" {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}

func requestHeaderValues(header http.Header, name string) []string {
	var values []string
	for key, entries := range header {
		if strings.EqualFold(key, name) {
			values = append(values, entries...)
		}
	}
	return values
}

func (s *Store) httpManifest() (ContextManifest, bool, error) {
	if s == nil {
		return ContextManifest{}, false, ErrInvalidStoreConfig
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == nil {
		return ContextManifest{}, s.sourceReachable, ErrNoContext
	}
	return cloneManifest(s.current.Manifest), s.sourceReachable, nil
}

func (s *Store) httpAsset(index int) (AssetManifest, []byte, bool, error) {
	if s == nil {
		return AssetManifest{}, nil, false, ErrInvalidStoreConfig
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == nil {
		return AssetManifest{}, nil, false, ErrNoContext
	}
	if index < 0 || index >= len(s.current.Manifest.Assets) {
		return AssetManifest{}, nil, false, nil
	}
	manifest := s.current.Manifest.Assets[index]
	data, ok := s.current.Assets[manifest.Digest]
	if !ok {
		return AssetManifest{}, nil, false, nil
	}
	return manifest, cloneBytes(data), true, nil
}

type healthResponse struct {
	ProtocolVersion int      `json:"protocol_version"`
	DeviceID        string   `json:"device_id"`
	DisplayName     string   `json:"display_name"`
	SourceDeviceID  string   `json:"source_device_id"`
	Revision        Revision `json:"revision"`
	HasContext      bool     `json:"has_context"`
}

func (h *httpHandler) serveHealth(w http.ResponseWriter) {
	manifest, _, err := h.options.Store.httpManifest()
	hasContext := err == nil
	if err != nil && !errors.Is(err, ErrNoContext) {
		writeHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	response := healthResponse{
		ProtocolVersion: ProtocolVersion,
		DeviceID:        h.options.LocalDevice.DeviceID,
		DisplayName:     h.options.LocalDevice.DisplayName,
		HasContext:      hasContext,
	}
	if hasContext {
		response.SourceDeviceID = manifest.SourceDeviceID
		response.Revision = manifest.Revision
	}
	writeHTTPJSON(w, http.StatusOK, response)
}

func (h *httpHandler) serveContext(w http.ResponseWriter) {
	manifest, _, err := h.options.Store.httpManifest()
	if errors.Is(err, ErrNoContext) {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	if err != nil {
		writeHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	writeHTTPJSON(w, http.StatusOK, manifest)
}

func (h *httpHandler) serveContextAsset(w http.ResponseWriter, index int) {
	h.serveSnapshotAsset(w, index)
}

func (h *httpHandler) serveLocalContextAsset(w http.ResponseWriter, index int) {
	h.serveSnapshotAsset(w, index)
}

func (h *httpHandler) serveSnapshotAsset(w http.ResponseWriter, index int) {
	manifest, data, ok, err := h.options.Store.httpAsset(index)
	if errors.Is(err, ErrNoContext) {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	if err != nil {
		writeHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	if !ok {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	if len(data) != manifest.ByteSize {
		writeHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", manifest.MIMEType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("X-MCPaste-SHA256", manifest.Digest)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *httpHandler) serveLocalContext(w http.ResponseWriter) {
	manifest, sourceReachable, err := h.options.Store.httpManifest()
	if errors.Is(err, ErrNoContext) {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	if err != nil {
		writeHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	syncState, ok := h.syncState(sourceReachable)
	if !ok {
		writeHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	writeHTTPJSON(w, http.StatusOK, LocalContextResponse{
		ContextManifest: manifest,
		SourceReachable: sourceReachable,
		SyncState:       syncState,
	})
}

func (h *httpHandler) syncState(sourceReachable bool) (state SyncState, ok bool) {
	if h.options.SyncState == nil {
		return "", false
	}
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	state = h.options.SyncState()
	switch state {
	case SyncUpToDate, SyncUpdating, SyncWaiting, SyncSourceOffline:
	default:
		return "", false
	}
	if !sourceReachable {
		return SyncSourceOffline, true
	}
	if state == SyncSourceOffline {
		return SyncWaiting, true
	}
	return state, true
}

func (h *httpHandler) serveLocalContextUpdate(w http.ResponseWriter, r *http.Request) {
	if !validJSONContentType(r) {
		writeHTTPError(w, http.StatusBadRequest)
		return
	}
	var wire localUpdateRequest
	if err := decodeHTTPObject(w, r, maxLocalContextBody, &wire); err != nil {
		writeHTTPError(w, statusForBodyError(err))
		return
	}
	update, err := wire.update()
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest)
		return
	}
	syncState, ok := h.syncState(true)
	if !ok {
		writeHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	manifest, err := safePublishLocal(h.options.Store, update)
	if err != nil {
		switch {
		case errors.Is(err, ErrLimitExceeded):
			writeHTTPError(w, http.StatusRequestEntityTooLarge)
		case errors.Is(err, ErrInvalidAsset):
			writeHTTPError(w, http.StatusBadRequest)
		case errors.Is(err, ErrClockExhausted), errors.Is(err, ErrRevisionConflict):
			writeHTTPError(w, http.StatusConflict)
		default:
			writeHTTPError(w, http.StatusServiceUnavailable)
		}
		return
	}
	writeHTTPJSON(w, http.StatusOK, PublicationResult{Revision: manifest.Revision, SyncState: syncState})
}

type localUpdateRequest struct {
	Text             string          `json:"text"`
	AssetDigests     []string        `json:"asset_digests"`
	ExpectedRevision json.RawMessage `json:"expected_revision"`
}

func (request localUpdateRequest) update() (LocalUpdate, error) {
	if request.ExpectedRevision == nil {
		return LocalUpdate{}, errHTTPInvalidBody
	}
	trimmed := bytes.TrimSpace(request.ExpectedRevision)
	var expected *Revision
	if !bytes.Equal(trimmed, []byte("null")) {
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return LocalUpdate{}, errHTTPInvalidBody
		}
		var revision Revision
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&revision); err != nil || revision.DeviceID == "" || revision.WallMillis == math.MaxInt64 {
			return LocalUpdate{}, errHTTPInvalidBody
		}
		var trailing json.RawMessage
		if err := decoder.Decode(&trailing); err != io.EOF {
			return LocalUpdate{}, errHTTPInvalidBody
		}
		expected = &revision
	}
	return LocalUpdate{
		Text:             request.Text,
		AssetDigests:     request.AssetDigests,
		ExpectedRevision: expected,
	}, nil
}

func safePublishLocal(store *Store, update LocalUpdate) (manifest ContextManifest, err error) {
	defer func() {
		if recover() != nil {
			err = ErrInvalidStoreConfig
		}
	}()
	return store.PublishLocal(update)
}

type assetUploadHeaders struct {
	mime        string
	contentSize int64
	width       int
	height      int
}

func (h *httpHandler) serveLocalAsset(w http.ResponseWriter, r *http.Request, digest string) {
	headers, status, ok := parseAssetUploadHeaders(r)
	if !ok {
		writeHTTPError(w, status)
		return
	}
	if r.Body == nil {
		writeHTTPError(w, http.StatusBadRequest)
		return
	}
	limited := http.MaxBytesReader(w, r.Body, headers.contentSize+1)
	bounded := io.LimitReader(limited, headers.contentSize+1)
	data, err := io.ReadAll(bounded)
	if err != nil {
		if isHTTPBodyTooLarge(err) {
			writeHTTPError(w, http.StatusRequestEntityTooLarge)
		} else {
			writeHTTPError(w, http.StatusBadRequest)
		}
		return
	}
	if int64(len(data)) < headers.contentSize {
		writeHTTPError(w, http.StatusBadRequest)
		return
	}
	if int64(len(data)) > headers.contentSize {
		writeHTTPError(w, http.StatusRequestEntityTooLarge)
		return
	}
	if err := safeStageAsset(h.options.Store, AssetManifest{
		Digest:   digest,
		MIMEType: headers.mime,
		Width:    headers.width,
		Height:   headers.height,
		ByteSize: len(data),
	}, data); err != nil {
		if errors.Is(err, ErrLimitExceeded) {
			writeHTTPError(w, http.StatusRequestEntityTooLarge)
		} else if errors.Is(err, ErrInvalidAsset) {
			writeHTTPError(w, http.StatusBadRequest)
		} else {
			writeHTTPError(w, http.StatusServiceUnavailable)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseAssetUploadHeaders(r *http.Request) (assetUploadHeaders, int, bool) {
	contentTypes := requestHeaderValues(r.Header, "Content-Type")
	if len(contentTypes) != 1 || (contentTypes[0] != "image/png" && contentTypes[0] != "image/jpeg") {
		return assetUploadHeaders{}, http.StatusBadRequest, false
	}
	contentLength, status, ok := requestContentLength(r)
	if !ok {
		return assetUploadHeaders{}, status, false
	}
	width, status, ok := requestPositiveIntHeader(r, "X-MCPaste-Width")
	if !ok {
		return assetUploadHeaders{}, status, false
	}
	height, status, ok := requestPositiveIntHeader(r, "X-MCPaste-Height")
	if !ok {
		return assetUploadHeaders{}, status, false
	}
	return assetUploadHeaders{mime: contentTypes[0], contentSize: contentLength, width: width, height: height}, http.StatusOK, true
}

func requestContentLength(r *http.Request) (int64, int, bool) {
	values := requestHeaderValues(r.Header, "Content-Length")
	if len(values) > 1 {
		return 0, http.StatusBadRequest, false
	}
	var length int64
	if len(values) == 1 {
		parsed, ok := parseDecimalUint(values[0])
		if !ok || parsed > math.MaxInt64 {
			return 0, http.StatusBadRequest, false
		}
		length = int64(parsed)
		if r.ContentLength >= 0 && r.ContentLength != length {
			return 0, http.StatusBadRequest, false
		}
	} else {
		if r.ContentLength < 0 {
			return 0, http.StatusBadRequest, false
		}
		length = r.ContentLength
	}
	if length <= 0 {
		return 0, http.StatusBadRequest, false
	}
	if length > MaxAssetBytes {
		return 0, http.StatusRequestEntityTooLarge, false
	}
	return length, http.StatusOK, true
}

func requestPositiveIntHeader(r *http.Request, name string) (int, int, bool) {
	values := requestHeaderValues(r.Header, name)
	if len(values) != 1 {
		return 0, http.StatusBadRequest, false
	}
	parsed, ok := parseDecimalUint(values[0])
	if !ok || parsed == 0 || parsed > uint64(maxHTTPInt()) {
		return 0, http.StatusBadRequest, false
	}
	return int(parsed), http.StatusOK, true
}

func parseDecimalUint(value string) (uint64, bool) {
	if !allDecimal(value) {
		return 0, false
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	return parsed, err == nil
}

func maxHTTPInt() int {
	return int(^uint(0) >> 1)
}

func safeStageAsset(store *Store, manifest AssetManifest, data []byte) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrInvalidStoreConfig
		}
	}()
	return store.StageAsset(manifest, data)
}

type announceEnvelope struct {
	Revision Revision `json:"revision"`
}

func (h *httpHandler) serveAnnounce(w http.ResponseWriter, r *http.Request) {
	if !validJSONContentType(r) {
		writeHTTPError(w, http.StatusBadRequest)
		return
	}
	var envelope announceEnvelope
	if err := decodeHTTPObject(w, r, maxAnnounceBodyBytes, &envelope); err != nil {
		writeHTTPError(w, statusForBodyError(err))
		return
	}
	if envelope.Revision.DeviceID == "" || envelope.Revision.WallMillis == math.MaxInt64 {
		writeHTTPError(w, http.StatusBadRequest)
		return
	}
	if revisionTooFarAhead(envelope.Revision.WallMillis, h.options.Store.now().UnixMilli()) {
		writeHTTPError(w, http.StatusBadRequest)
		return
	}
	manifest, _, err := h.options.Store.httpManifest()
	hasCurrent := err == nil
	if err != nil && !errors.Is(err, ErrNoContext) {
		writeHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	if hasCurrent && envelope.Revision.Compare(manifest.Revision) <= 0 {
		writeHTTPError(w, http.StatusConflict)
		return
	}
	if err := h.invokeAnnounce(r.Context(), envelope.Revision); err != nil {
		writeHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func safeAnnounce(callback func(context.Context, Revision) error, ctx context.Context, revision Revision) (err error) {
	defer func() {
		if recover() != nil {
			err = errHTTPOperation
		}
	}()
	return callback(ctx, revision)
}

func (h *httpHandler) invokeAnnounce(requestContext context.Context, revision Revision) error {
	h.announceMu.Lock()
	call := h.announceActive
	if call != nil {
		if call.revision != revision {
			h.announceMu.Unlock()
			return errHTTPOperation
		}
		call.followers++
		h.announceMu.Unlock()
		return waitForAnnounce(call, requestContext)
	}
	call = &announceCall{
		revision:     revision,
		publicDone:   make(chan struct{}),
		callbackDone: make(chan struct{}),
	}
	h.announceActive = call
	callback := h.options.Announce
	h.announceMu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	callbackContext, cancel := context.WithDeadline(context.Background(), deadline)
	deadlineTimer := time.AfterFunc(time.Until(deadline), func() {
		call.resolvePublicResult(errHTTPOperation)
		cancel()
	})
	go func() {
		err := safeAnnounce(callback, callbackContext, revision)
		if callbackContext.Err() != nil {
			err = errHTTPOperation
		}
		deadlineTimer.Stop()
		cancel()
		h.announceMu.Lock()
		if h.announceActive == call {
			h.announceActive = nil
		}
		call.resolvePublicResult(err)
		h.announceMu.Unlock()
		close(call.callbackDone)
	}()
	return waitForAnnounce(call, requestContext)
}

func waitForAnnounce(call *announceCall, requestContext context.Context) error {
	select {
	case <-call.publicDone:
		return call.resultErr
	case <-requestContext.Done():
		return errHTTPOperation
	}
}

func (h *httpHandler) serveDevices(w http.ResponseWriter) {
	manifest, _, err := h.options.Store.httpManifest()
	hasContext := err == nil
	if err != nil && !errors.Is(err, ErrNoContext) {
		writeHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	peers, ok := safeRegistryList(h.options.Registry)
	if !ok {
		writeHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	currentSourceID := ""
	if hasContext {
		currentSourceID = manifest.SourceDeviceID
	}
	devices := make([]deviceResponse, 0, len(peers)+1)
	local := h.options.LocalDevice
	devices = append(devices, deviceResponse{
		ID:          local.DeviceID,
		DisplayName: local.DisplayName,
		Reachable:   true,
		IsLocal:     true,
		IsSource:    hasContext && currentSourceID == local.DeviceID,
		LastSeenAt:  local.LastSeenAt.UTC(),
	})
	seen := map[string]struct{}{local.DeviceID: {}}
	for _, peer := range peers {
		if _, exists := seen[peer.DeviceID]; exists {
			continue
		}
		seen[peer.DeviceID] = struct{}{}
		devices = append(devices, deviceResponse{
			ID:          peer.DeviceID,
			DisplayName: peer.DisplayName,
			Reachable:   peerReachable(peer, h.options.ReachablePeers),
			IsLocal:     false,
			IsSource:    hasContext && currentSourceID == peer.DeviceID,
			LastSeenAt:  peer.LastSeenAt.UTC(),
		})
	}
	sort.SliceStable(devices[1:], func(i, j int) bool {
		left, right := devices[i+1], devices[j+1]
		if left.DisplayName != right.DisplayName {
			return left.DisplayName < right.DisplayName
		}
		return left.ID < right.ID
	})
	writeHTTPJSON(w, http.StatusOK, struct {
		Devices []deviceResponse `json:"devices"`
	}{Devices: devices})
}

type deviceResponse struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	Reachable   bool      `json:"reachable"`
	IsLocal     bool      `json:"is_local"`
	IsSource    bool      `json:"is_source"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

func safeRegistryList(registry *Registry) (peers []KnownPeer, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return registry.List(), true
}

func peerReachable(peer KnownPeer, allowed *AllowedPeerIPs) bool {
	for _, raw := range peer.Addresses {
		address, err := netip.ParseAddr(raw)
		if err == nil && address.IsValid() && address.Zone() == "" && allowed.Contains(address) {
			return true
		}
	}
	return false
}

func decodeHTTPObject(w http.ResponseWriter, r *http.Request, limit int64, destination any) error {
	if r == nil || r.Body == nil {
		return errHTTPInvalidBody
	}
	if r.ContentLength > limit {
		return errHTTPBodyTooBig
	}
	limited := http.MaxBytesReader(w, r.Body, limit+1)
	body, err := io.ReadAll(io.LimitReader(limited, limit+1))
	if err != nil {
		if isHTTPBodyTooLarge(err) {
			return errHTTPBodyTooBig
		}
		return errHTTPInvalidBody
	}
	if int64(len(body)) > limit {
		return errHTTPBodyTooBig
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		if isHTTPBodyTooLarge(err) {
			return errHTTPBodyTooBig
		}
		return errHTTPInvalidBody
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if isHTTPBodyTooLarge(err) {
			return errHTTPBodyTooBig
		}
		return errHTTPInvalidBody
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errHTTPInvalidBody
	}
	strict := json.NewDecoder(bytes.NewReader(trimmed))
	strict.DisallowUnknownFields()
	if err := strict.Decode(destination); err != nil {
		return errHTTPInvalidBody
	}
	return nil
}

func validJSONContentType(r *http.Request) bool {
	if r == nil {
		return false
	}
	values := requestHeaderValues(r.Header, "Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(values[0])
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func isHTTPBodyTooLarge(err error) bool {
	var maxBytesError *http.MaxBytesError
	return errors.As(err, &maxBytesError)
}

func statusForBodyError(err error) int {
	if errors.Is(err, errHTTPBodyTooBig) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

func writeHTTPJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		writeHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func writeHTTPError(w http.ResponseWriter, status int) {
	message := "request failed"
	switch status {
	case http.StatusBadRequest:
		message = "bad request"
	case http.StatusUnauthorized:
		message = "unauthorized"
	case http.StatusForbidden:
		message = "forbidden"
	case http.StatusNotFound:
		message = "not found"
	case http.StatusMethodNotAllowed:
		message = "method not allowed"
	case http.StatusConflict:
		message = "conflict"
	case http.StatusRequestEntityTooLarge:
		message = "request too large"
	case http.StatusServiceUnavailable:
		message = "service unavailable"
	}
	writeHTTPJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: message})
}
