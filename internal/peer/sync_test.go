package peer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testPeerPort = 38421

type testStatusSource func(context.Context) ([]TailnetCandidate, error)

func (source testStatusSource) Status(ctx context.Context) ([]TailnetCandidate, error) {
	return source(ctx)
}

type testRuntime struct {
	store               *Store
	server              *httptest.Server
	coordinator         *Coordinator
	reachable           atomic.Bool
	deviceID            string
	displayName         string
	address             string
	announces           atomic.Int64
	protocol            atomic.Int64
	contextProto        atomic.Int64
	failAssetIndex      atomic.Int64
	assetFetchSucceeded chan int
	failedAssetRelease  chan struct{}
	announceStatus      atomic.Int64
	announceStarted     chan struct{}
	announceRelease     chan struct{}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type clusterTransport struct {
	mu      sync.RWMutex
	targets map[string]*httptest.Server
}

func (transport *clusterTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.RLock()
	target := transport.targets[request.URL.Hostname()]
	transport.mu.RUnlock()
	if target == nil {
		return nil, errors.New("unknown test peer")
	}
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		return nil, errors.New("invalid test peer URL")
	}
	clone := request.Clone(request.Context())
	clone.URL.Scheme = targetURL.Scheme
	clone.URL.Host = targetURL.Host
	return target.Client().Transport.RoundTrip(clone)
}

func newRuntimeCluster(t *testing.T, count int) []*testRuntime {
	t.Helper()
	if count < 1 || count > 3 {
		t.Fatalf("invalid test cluster size %d", count)
	}

	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	cluster := make([]*testRuntime, count)
	transport := &clusterTransport{targets: make(map[string]*httptest.Server)}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	for index := range cluster {
		letter := byte('a' + index)
		deviceID := fmt.Sprintf("%c%c%c%c%c%c%c%c-%c%c%c%c-%c%c%c%c-%c%c%c%c-%c%c%c%c%c%c%c%c%c%c%c%c",
			letter, letter, letter, letter, letter, letter, letter, letter,
			letter, letter, letter, letter, letter, letter, letter, letter,
			letter, letter, letter, letter, letter, letter, letter, letter,
			letter, letter, letter, letter, letter, letter, letter, letter)
		store, err := NewStore(deviceID, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		runtime := &testRuntime{
			store:               store,
			deviceID:            deviceID,
			displayName:         "Test Peer " + strings.ToUpper(string(letter)),
			address:             fmt.Sprintf("100.64.0.%d", index+1),
			assetFetchSucceeded: make(chan int, MaxAssets),
			announceStarted:     make(chan struct{}, 1),
		}
		runtime.reachable.Store(true)
		runtime.protocol.Store(ProtocolVersion)
		runtime.contextProto.Store(ProtocolVersion)
		runtime.failAssetIndex.Store(-1)
		runtime.server = httptest.NewServer(http.HandlerFunc(runtime.serveHTTP))
		t.Cleanup(runtime.server.Close)
		transport.targets[runtime.address] = runtime.server
		cluster[index] = runtime
	}

	for _, runtime := range cluster {
		current := runtime
		status := testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
			candidates := make([]TailnetCandidate, 0, len(cluster)-1)
			for _, peer := range cluster {
				if peer == current {
					continue
				}
				candidates = append(candidates, TailnetCandidate{
					Name:      peer.displayName,
					Addresses: []string{peer.address},
				})
			}
			return candidates, nil
		})
		registryPath := filepath.Join(t.TempDir(), "peers.json")
		allowedPeers := &AllowedPeerIPs{}
		reachablePeers := &AllowedPeerIPs{}
		coordinator, err := NewCoordinator(CoordinatorOptions{
			DeviceID:       runtime.deviceID,
			Port:           testPeerPort,
			Store:          runtime.store,
			Registry:       NewRegistry(registryPath),
			AllowedPeers:   allowedPeers,
			ReachablePeers: reachablePeers,
			Tailscale:      status,
			Now:            func() time.Time { return now },
			HTTPClient:     client,
		})
		if err != nil {
			t.Fatal(err)
		}
		runtime.coordinator = coordinator
	}
	return cluster
}

func (runtime *testRuntime) serveHTTP(w http.ResponseWriter, request *http.Request) {
	if !runtime.reachable.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/v1/health":
		response := healthResponse{
			ProtocolVersion: int(runtime.protocol.Load()),
			DeviceID:        runtime.deviceID,
			DisplayName:     runtime.displayName,
		}
		if manifest, err := runtime.store.Manifest(); err == nil {
			response.HasContext = true
			response.SourceDeviceID = manifest.SourceDeviceID
			response.Revision = manifest.Revision
		}
		writeTestJSON(w, response)
	case request.Method == http.MethodGet && request.URL.Path == peerContextRoute:
		manifest, err := runtime.store.Manifest()
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		manifest.ProtocolVersion = int(runtime.contextProto.Load())
		writeTestJSON(w, manifest)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, peerContextAssetsBase):
		index, err := strconv.Atoi(strings.TrimPrefix(request.URL.Path, peerContextAssetsBase))
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if int64(index) == runtime.failAssetIndex.Load() {
			if runtime.failedAssetRelease != nil {
				select {
				case <-runtime.failedAssetRelease:
				case <-request.Context().Done():
					return
				}
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		manifest, data, ok, err := runtime.store.httpAsset(index)
		if err != nil || !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", manifest.MIMEType)
		w.Header().Set("X-MCPaste-SHA256", manifest.Digest)
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(data); err == nil {
			select {
			case runtime.assetFetchSucceeded <- index:
			default:
			}
		}
	case request.Method == http.MethodPost && request.URL.Path == "/v1/announce":
		var envelope announceEnvelope
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		runtime.announces.Add(1)
		select {
		case runtime.announceStarted <- struct{}{}:
		default:
		}
		if runtime.announceRelease != nil {
			select {
			case <-runtime.announceRelease:
			case <-request.Context().Done():
				return
			}
		}
		if status := int(runtime.announceStatus.Load()); status != 0 {
			w.WriteHeader(status)
			return
		}
		if runtime.coordinator == nil || runtime.coordinator.HandleAnnouncement(request.Context(), envelope.Revision) != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func writeTestJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func (runtime *testRuntime) publish(t *testing.T, text string) {
	t.Helper()
	runtime.publishWithoutAnnounce(t, text)
	runtime.poll(t)
}

func (runtime *testRuntime) publishWithoutAnnounce(t *testing.T, text string) {
	t.Helper()
	var expected *Revision
	manifest, err := runtime.store.Manifest()
	if err == nil {
		expected = revisionPtr(manifest.Revision)
	} else if !errors.Is(err, ErrNoContext) {
		t.Fatal(err)
	}
	if _, err := runtime.store.PublishLocal(LocalUpdate{Text: text, ExpectedRevision: expected}); err != nil {
		t.Fatal(err)
	}
}

func (runtime *testRuntime) poll(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.coordinator.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
}

func (runtime *testRuntime) manifest(t *testing.T) ContextManifest {
	t.Helper()
	manifest, err := runtime.store.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func (runtime *testRuntime) setReachable(reachable bool) {
	runtime.reachable.Store(reachable)
}

func clusterTick(t *testing.T, runtimes ...*testRuntime) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		for _, runtime := range runtimes {
			runtime.poll(t)
		}
		want := runtimes[0].manifest(t).Revision
		converged := true
		for _, runtime := range runtimes[1:] {
			if runtime.manifest(t).Revision != want {
				converged = false
				break
			}
		}
		if converged {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("cluster did not converge")
		}
	}
}

func TestThreePeersConvergeOnHighestRevision(t *testing.T) {
	cluster := newRuntimeCluster(t, 3)
	a, b, c := cluster[0], cluster[1], cluster[2]
	a.publish(t, "from-a")
	b.publish(t, "from-b")
	clusterTick(t, a, b, c)
	want := b.manifest(t).Revision
	for _, runtime := range []*testRuntime{a, b, c} {
		got := runtime.manifest(t)
		if got.Revision != want || got.Text != "from-b" {
			t.Fatalf("manifest = %#v", got)
		}
	}
}

func TestMissedAnnouncementConvergesOnHealthPoll(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]
	a.publishWithoutAnnounce(t, "eventually")
	b.poll(t)
	if got := b.manifest(t).Text; got != "eventually" {
		t.Fatalf("text = %q", got)
	}
}

func TestSourceOfflineBlocksConnectorUntilSourceReturns(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]
	a.publish(t, "remote")
	b.poll(t)
	a.setReachable(false)
	b.poll(t)
	if _, err := b.store.ConnectorSnapshot(); !errors.Is(err, ErrSourceOffline) {
		t.Fatalf("error = %v", err)
	}
	a.setReachable(true)
	b.poll(t)
	if _, err := b.store.ConnectorSnapshot(); err != nil {
		t.Fatal(err)
	}
}

func TestSimultaneousEqualWallTimeEditsUseDeviceIDTieBreak(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]
	a.publishWithoutAnnounce(t, "from-a")
	b.publishWithoutAnnounce(t, "from-b")

	clusterTick(t, a, b)

	want := b.manifest(t).Revision
	if got := a.manifest(t); got.Revision != want || got.Text != "from-b" {
		t.Fatalf("manifest = %#v", got)
	}
}

func TestOfflineEditsConvergeAfterReconnect(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]
	a.publish(t, "base")
	b.poll(t)
	a.setReachable(false)
	a.publishWithoutAnnounce(t, "offline-a")
	b.publishWithoutAnnounce(t, "offline-b")
	a.setReachable(true)

	clusterTick(t, a, b)

	for _, runtime := range cluster {
		if got := runtime.manifest(t).Text; got != "offline-b" {
			t.Fatalf("text = %q", got)
		}
	}
}

func TestAdoptedRevisionRelaysOnceWithoutAnnouncementLoop(t *testing.T) {
	cluster := newRuntimeCluster(t, 3)
	a, b, c := cluster[0], cluster[1], cluster[2]
	a.publishWithoutAnnounce(t, "relay")

	b.poll(t)
	if got := c.manifest(t).Text; got != "relay" {
		t.Fatalf("relayed text = %q", got)
	}
	wantAnnouncements := totalAnnouncements(cluster)
	if wantAnnouncements == 0 {
		t.Fatal("revision was not relayed")
	}

	for _, runtime := range cluster {
		runtime.poll(t)
	}
	if got := totalAnnouncements(cluster); got != wantAnnouncements {
		t.Fatalf("announcement count = %d, want %d", got, wantAnnouncements)
	}
}

func TestAnnouncementOutcomeControlsWaitingAndUpdating(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]
	a.publishWithoutAnnounce(t, "local-winner")
	b.announceStatus.Store(http.StatusOK)

	a.poll(t)
	if got := a.coordinator.SyncState(); got != SyncWaiting {
		t.Fatalf("state after non-contract status = %q, want %q", got, SyncWaiting)
	}

	b.announceStatus.Store(http.StatusNoContent)
	a.poll(t)
	if got := a.coordinator.SyncState(); got != SyncUpdating {
		t.Fatalf("state after successful unconfirmed announce = %q, want %q", got, SyncUpdating)
	}
}

func TestPendingPropagationPersistsUntilHealthConfirmation(t *testing.T) {
	cluster := newRuntimeCluster(t, 3)
	a, b, c := cluster[0], cluster[1], cluster[2]
	c.setReachable(false)

	a.publishWithoutAnnounce(t, "pending-winner")
	b.announceStatus.Store(http.StatusNoContent)
	a.poll(t)
	if got := a.coordinator.SyncState(); got != SyncUpdating {
		t.Fatalf("first state = %q, want %q after exact 204", got, SyncUpdating)
	}

	b.announceStatus.Store(http.StatusInternalServerError)
	a.poll(t)
	if got := a.coordinator.SyncState(); got != SyncUpdating {
		t.Fatalf("second state = %q, want %q while prior propagation is pending", got, SyncUpdating)
	}

	adoptCurrentSnapshot(t, a.store, b.store)
	a.poll(t)
	if got := a.coordinator.SyncState(); got != SyncUpToDate {
		t.Fatalf("confirmed state = %q, want %q", got, SyncUpToDate)
	}

	c.announceStatus.Store(http.StatusInternalServerError)
	c.setReachable(true)
	a.poll(t)
	if got := a.coordinator.SyncState(); got != SyncWaiting {
		t.Fatalf("post-confirmation state = %q, want %q after pending propagation clears", got, SyncWaiting)
	}
}

func TestNewLocalWinnerClearsPendingPropagation(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]

	a.publishWithoutAnnounce(t, "first-winner")
	b.announceStatus.Store(http.StatusNoContent)
	a.poll(t)
	if got := a.coordinator.SyncState(); got != SyncUpdating {
		t.Fatalf("first state = %q, want %q after exact 204", got, SyncUpdating)
	}

	b.announceStatus.Store(http.StatusInternalServerError)
	a.publishWithoutAnnounce(t, "new-winner")
	a.poll(t)
	if got := a.coordinator.SyncState(); got != SyncWaiting {
		t.Fatalf("superseding state = %q, want %q after all announces fail", got, SyncWaiting)
	}
}

func TestNoReachablePeersClearPendingPropagation(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]

	a.publishWithoutAnnounce(t, "vacuous-confirmation")
	b.announceStatus.Store(http.StatusNoContent)
	a.poll(t)
	if got := a.coordinator.SyncState(); got != SyncUpdating {
		t.Fatalf("first state = %q, want %q after exact 204", got, SyncUpdating)
	}

	b.setReachable(false)
	a.poll(t)
	if got := a.coordinator.SyncState(); got != SyncUpToDate {
		t.Fatalf("offline-peer state = %q, want %q", got, SyncUpToDate)
	}

	b.announceStatus.Store(http.StatusInternalServerError)
	b.setReachable(true)
	a.poll(t)
	if got := a.coordinator.SyncState(); got != SyncWaiting {
		t.Fatalf("post-confirmation state = %q, want %q after pending propagation clears", got, SyncWaiting)
	}
}

func TestPendingPropagationSurvivesFailedHigherRevisionAdoption(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]

	a.publishWithoutAnnounce(t, "pending-winner")
	pending := a.manifest(t).Revision
	b.announceStatus.Store(http.StatusNoContent)
	a.poll(t)
	if got := a.coordinator.SyncState(); got != SyncUpdating {
		t.Fatalf("first state = %q, want %q after exact 204", got, SyncUpdating)
	}
	assertPendingPropagation(t, a.coordinator, pending, true)

	b.publishWithoutAnnounce(t, "higher-winner")
	higher := b.manifest(t).Revision
	if higher.Compare(pending) <= 0 {
		t.Fatalf("higher revision = %#v, want greater than pending %#v", higher, pending)
	}
	b.contextProto.Store(ProtocolVersion + 1)
	a.poll(t)
	if got := a.manifest(t).Revision; got != pending {
		t.Fatalf("revision after failed adoption = %#v, want pending %#v", got, pending)
	}
	if got := a.coordinator.SyncState(); got != SyncUpdating {
		t.Fatalf("failed-adoption state = %q, want %q", got, SyncUpdating)
	}
	assertPendingPropagation(t, a.coordinator, pending, true)

	b.contextProto.Store(ProtocolVersion)
	a.poll(t)
	if got := a.manifest(t).Revision; got != higher {
		t.Fatalf("revision after successful adoption = %#v, want %#v", got, higher)
	}
	if got := a.coordinator.SyncState(); got != SyncUpToDate {
		t.Fatalf("successful-adoption state = %q, want %q", got, SyncUpToDate)
	}
	assertPendingPropagation(t, a.coordinator, Revision{}, false)
}

func TestFetchUpdatingEndsBeforeRelayStarts(t *testing.T) {
	cluster := newRuntimeCluster(t, 3)
	a, b, c := cluster[0], cluster[1], cluster[2]
	a.publishWithoutAnnounce(t, "remote-winner")
	c.announceRelease = make(chan struct{})
	pollDone := make(chan error, 1)
	go func() { pollDone <- b.coordinator.PollOnce(context.Background()) }()

	select {
	case <-c.announceStarted:
	case <-time.After(time.Second):
		t.Fatal("relay announcement did not start")
	}
	if got := b.coordinator.SyncState(); got != SyncWaiting {
		t.Fatalf("state during pre-success relay = %q, want %q", got, SyncWaiting)
	}
	close(c.announceRelease)
	if err := <-pollDone; err != nil {
		t.Fatal(err)
	}
	if got := b.coordinator.SyncState(); got != SyncUpdating {
		t.Fatalf("state after successful unconfirmed relay = %q, want %q", got, SyncUpdating)
	}

	b.poll(t)
	if got := b.coordinator.SyncState(); got != SyncUpToDate {
		t.Fatalf("state after health confirmation = %q, want %q", got, SyncUpToDate)
	}
}

func totalAnnouncements(cluster []*testRuntime) int64 {
	var total int64
	for _, runtime := range cluster {
		total += runtime.announces.Load()
	}
	return total
}

func adoptCurrentSnapshot(t *testing.T, source, target *Store) {
	t.Helper()
	snapshot, err := source.ConnectorSnapshot()
	if err != nil {
		t.Fatalf("ConnectorSnapshot: %v", err)
	}
	if err := target.AdoptRemote(snapshot.Manifest, snapshot.Assets); err != nil {
		t.Fatalf("AdoptRemote: %v", err)
	}
}

func assertPendingPropagation(t *testing.T, coordinator *Coordinator, want Revision, wantPresent bool) {
	t.Helper()
	coordinator.stateMu.RLock()
	defer coordinator.stateMu.RUnlock()
	if coordinator.hasPendingPropagation != wantPresent || coordinator.pendingPropagation != want {
		t.Fatalf("pending propagation = (%#v, %t), want (%#v, %t)", coordinator.pendingPropagation, coordinator.hasPendingPropagation, want, wantPresent)
	}
}

func TestPartialAssetFetchKeepsPriorSnapshot(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]
	priorData := []byte("prior-complete-asset")
	priorAsset := AssetManifest{
		Digest:   sha256Hex(priorData),
		MIMEType: "image/png",
		Width:    1,
		Height:   1,
		ByteSize: len(priorData),
	}
	if err := a.store.StageAsset(priorAsset, priorData); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.PublishLocal(LocalUpdate{Text: "prior", AssetDigests: []string{priorAsset.Digest}}); err != nil {
		t.Fatal(err)
	}
	b.poll(t)
	prior, err := b.store.ConnectorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	a.assetFetchSucceeded = make(chan int, MaxAssets)

	newData := [][]byte{[]byte("new-first-asset"), []byte("new-second-asset")}
	newAssets := make([]AssetManifest, len(newData))
	for index, data := range newData {
		newAssets[index] = AssetManifest{
			Digest:   sha256Hex(data),
			MIMEType: "image/png",
			Width:    index + 1,
			Height:   index + 1,
			ByteSize: len(data),
		}
		if err := a.store.StageAsset(newAssets[index], data); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.store.PublishLocal(LocalUpdate{
		Text:             "partial",
		AssetDigests:     []string{newAssets[0].Digest, newAssets[1].Digest},
		ExpectedRevision: revisionPtr(a.manifest(t).Revision),
	}); err != nil {
		t.Fatal(err)
	}
	a.failAssetIndex.Store(1)
	a.failedAssetRelease = make(chan struct{})
	pollDone := make(chan error, 1)
	go func() { pollDone <- b.coordinator.PollOnce(context.Background()) }()

	select {
	case index := <-a.assetFetchSucceeded:
		if index != 0 {
			t.Fatalf("successful asset index = %d, want 0", index)
		}
	case <-time.After(time.Second):
		t.Fatal("first asset did not complete before the failing asset")
	}
	close(a.failedAssetRelease)
	if err := <-pollDone; err != nil {
		t.Fatal(err)
	}

	got, err := b.store.ConnectorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, prior) {
		t.Fatalf("snapshot after mixed partial fetch = %#v, want %#v", got, prior)
	}
	if got := b.coordinator.SyncState(); got == SyncUpdating {
		t.Fatalf("sync state remained %q", got)
	}
}

func TestProtocolMismatchIsNotRecordedOrAdopted(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]
	b.publishWithoutAnnounce(t, "compatible")
	a.publishWithoutAnnounce(t, "older")
	a.publishWithoutAnnounce(t, "incompatible")
	a.protocol.Store(ProtocolVersion + 1)

	b.poll(t)

	if got := b.manifest(t).Text; got != "compatible" {
		t.Fatalf("text = %q", got)
	}
	for _, known := range b.coordinator.registry.List() {
		if known.DeviceID == a.deviceID {
			t.Fatal("protocol-mismatched identity was recorded")
		}
	}
}

func TestMalformedHealthIdentityIsNotUsed(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]
	b.publishWithoutAnnounce(t, "local")
	a.publishWithoutAnnounce(t, "older")
	a.publishWithoutAnnounce(t, "malformed")
	a.deviceID = "not-a-device-id"

	b.poll(t)

	if got := b.manifest(t).Text; got != "local" {
		t.Fatalf("text = %q", got)
	}
}

func TestIPv6PeerURLUsesBracketedHostPort(t *testing.T) {
	const address = "fd7a:115c:a1e0::2"
	const remoteID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	store, err := NewStore("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var requestedHost string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestedHost = request.URL.Host
		if request.Header.Get("Authorization") != "" {
			t.Error("peer request carried loopback authorization")
		}
		body := `{"protocol_version":1,"device_id":"` + remoteID + `","display_name":"IPv6 Test Peer","source_device_id":"","revision":{"wall_millis":0,"logical":0,"device_id":""},"has_context":false}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
	coordinator, err := NewCoordinator(CoordinatorOptions{
		DeviceID:       "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		Port:           testPeerPort,
		Store:          store,
		Registry:       NewRegistry(filepath.Join(t.TempDir(), "peers.json")),
		AllowedPeers:   &AllowedPeerIPs{},
		ReachablePeers: &AllowedPeerIPs{},
		Tailscale: testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
			return []TailnetCandidate{{Name: "ipv6", Addresses: []string{address}}}, nil
		}),
		Now:        time.Now,
		HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requestedHost != "["+address+"]:"+strconv.Itoa(testPeerPort) {
		t.Fatalf("request host = %q", requestedHost)
	}
}

func TestSourceRestartRehydratesFromLiveReplica(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]
	a.publish(t, "survives-restart")
	b.poll(t)

	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	restarted, err := NewStore(a.deviceID, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	a.store = restarted
	a.coordinator.store = restarted

	a.poll(t)

	if got := a.manifest(t); got.Text != "survives-restart" || got.SourceDeviceID != a.deviceID {
		t.Fatalf("rehydrated manifest = %#v", got)
	}
	if _, err := a.store.ConnectorSnapshot(); err != nil {
		t.Fatal(err)
	}
}

func TestHealthProbesUseAtMostFourWorkers(t *testing.T) {
	const candidateCount = 5
	started := make(chan struct{}, candidateCount)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	var active atomic.Int64
	var maximum atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		current := active.Add(1)
		defer active.Add(-1)
		updateMaximum(&maximum, current)
		started <- struct{}{}
		select {
		case <-release:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
		host := request.URL.Hostname()
		suffix := host[len(host)-1:]
		body := `{"protocol_version":1,"device_id":"` + strings.Repeat(suffix, 8) + `-` + strings.Repeat(suffix, 4) + `-` + strings.Repeat(suffix, 4) + `-` + strings.Repeat(suffix, 4) + `-` + strings.Repeat(suffix, 12) + `","display_name":"Probe Test Peer","source_device_id":"","revision":{"wall_millis":0,"logical":0,"device_id":""},"has_context":false}`
		return testHTTPResponse(request, http.StatusOK, body), nil
	})}
	candidates := make([]TailnetCandidate, 0, candidateCount)
	for index := 1; index <= candidateCount; index++ {
		candidates = append(candidates, TailnetCandidate{Addresses: []string{fmt.Sprintf("100.64.0.%d", index)}})
	}
	coordinator := newTestCoordinator(t, client, testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
		return candidates, nil
	}))
	done := make(chan error, 1)
	go func() { done <- coordinator.PollOnce(context.Background()) }()

	waitForSignals(t, started, 4)
	if got := maximum.Load(); got != 4 {
		t.Fatalf("maximum concurrent probes = %d, want 4", got)
	}
	select {
	case <-started:
		t.Fatal("fifth probe started before a worker was released")
	default:
	}
	releaseAll()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAssetFetchUsesAtMostThreeWorkers(t *testing.T) {
	assets := make([]AssetManifest, 4)
	assetData := make([][]byte, 4)
	for index := range assets {
		data := []byte(fmt.Sprintf("asset-%d", index))
		assetData[index] = data
		assets[index] = AssetManifest{Digest: sha256Hex(data), MIMEType: "image/png", Width: 1, Height: 1, ByteSize: len(data)}
	}
	manifest := ContextManifest{
		ProtocolVersion: ProtocolVersion,
		Revision:        Revision{WallMillis: 1, DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},
		SourceDeviceID:  "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		UpdatedAt:       time.UnixMilli(1).UTC(),
		Text:            "assets",
		Assets:          assets,
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, len(assets))
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	var active atomic.Int64
	var maximum atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == peerContextRoute {
			return testHTTPResponse(request, http.StatusOK, string(manifestJSON)), nil
		}
		index, parseErr := strconv.Atoi(strings.TrimPrefix(request.URL.Path, peerContextAssetsBase))
		if parseErr != nil || index < 0 || index >= len(assetData) {
			return testHTTPResponse(request, http.StatusNotFound, ""), nil
		}
		current := active.Add(1)
		defer active.Add(-1)
		updateMaximum(&maximum, current)
		started <- struct{}{}
		select {
		case <-release:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
		return testHTTPResponse(request, http.StatusOK, string(assetData[index])), nil
	})}
	coordinator := newTestCoordinator(t, client, testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
		return nil, nil
	}))
	done := make(chan error, 1)
	go func() {
		_, _, fetchErr := coordinator.fetchSnapshot(context.Background(), probedPeer{baseURL: "http://100.64.0.2:38421"})
		done <- fetchErr
	}()

	waitForSignals(t, started, 3)
	if got := maximum.Load(); got != 3 {
		t.Fatalf("maximum concurrent asset fetches = %d, want 3", got)
	}
	select {
	case <-started:
		t.Fatal("fourth asset fetch started before a worker was released")
	default:
	}
	releaseAll()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStatusUnavailableSetsWaitingWithoutContext(t *testing.T) {
	coordinator := newTestCoordinator(t, nil, testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
		return nil, ErrTailscaleUnavailable
	}))
	if err := coordinator.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := coordinator.SyncState(); got != SyncWaiting {
		t.Fatalf("sync state = %q, want %q", got, SyncWaiting)
	}
}

func TestReachablePeersWithoutContextAreUpToDate(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a := cluster[0]

	a.poll(t)

	if got := a.coordinator.SyncState(); got != SyncUpToDate {
		t.Fatalf("sync state = %q, want %q", got, SyncUpToDate)
	}
}

func TestAllowedAddressSnapshotIsReplacedBeforeProbe(t *testing.T) {
	const address = "100.64.0.2"
	allowed := &AllowedPeerIPs{}
	reachable := &AllowedPeerIPs{}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		parsed := netip.MustParseAddr(address)
		if !allowed.Contains(parsed) {
			return nil, errors.New("candidate was not allowed before probe")
		}
		body := `{"protocol_version":1,"device_id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","display_name":"Allowed Test Peer","source_device_id":"","revision":{"wall_millis":0,"logical":0,"device_id":""},"has_context":false}`
		return testHTTPResponse(request, http.StatusOK, body), nil
	})}
	coordinator := newTestCoordinatorWithPeerSets(t, client, testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
		return []TailnetCandidate{{Addresses: []string{address}}}, nil
	}), allowed, reachable)
	if err := coordinator.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reachable.Contains(netip.MustParseAddr(address)) {
		t.Fatal("successfully probed address was not marked reachable")
	}
}

func TestFailedProbeRemainsAllowedButNotReachable(t *testing.T) {
	const address = "100.64.0.2"
	allowed := &AllowedPeerIPs{}
	reachable := &AllowedPeerIPs{}
	reachable.Replace([]netip.Addr{netip.MustParseAddr(address)})
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("MCPaste health unavailable")
	})}
	coordinator := newTestCoordinatorWithPeerSets(t, client, testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
		return []TailnetCandidate{{Addresses: []string{address}}}, nil
	}), allowed, reachable)

	if err := coordinator.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	parsed := netip.MustParseAddr(address)
	if !allowed.Contains(parsed) {
		t.Fatal("failed-probe Tailscale candidate was not authorized")
	}
	if reachable.Contains(parsed) {
		t.Fatal("failed-probe Tailscale candidate was marked reachable")
	}
}

func TestEmptyCandidatePollClearsReachability(t *testing.T) {
	address := netip.MustParseAddr("100.64.0.2")
	allowed := &AllowedPeerIPs{}
	reachable := &AllowedPeerIPs{}
	allowed.Replace([]netip.Addr{address})
	reachable.Replace([]netip.Addr{address})
	coordinator := newTestCoordinatorWithPeerSets(t, nil, testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
		return nil, nil
	}), allowed, reachable)

	if err := coordinator.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if allowed.Contains(address) || reachable.Contains(address) {
		t.Fatal("empty candidate poll retained stale authorization or reachability")
	}
}

func TestStatusFailureClearsAuthorizationAndReachability(t *testing.T) {
	const address = "100.64.0.2"
	allowed := &AllowedPeerIPs{}
	reachable := &AllowedPeerIPs{}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"protocol_version":1,"device_id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","display_name":"Allowed Test Peer","source_device_id":"","revision":{"wall_millis":0,"logical":0,"device_id":""},"has_context":false}`
		return testHTTPResponse(request, http.StatusOK, body), nil
	})}
	statusCalls := 0
	coordinator := newTestCoordinatorWithPeerSets(t, client, testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
		statusCalls++
		if statusCalls == 1 {
			return []TailnetCandidate{{Addresses: []string{address}}}, nil
		}
		return nil, ErrTailscaleUnavailable
	}), allowed, reachable)

	if err := coordinator.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	parsed := netip.MustParseAddr(address)
	if !allowed.Contains(parsed) {
		t.Fatal("discovered address was not authorized")
	}
	if !reachable.Contains(parsed) {
		t.Fatal("successfully probed address was not reachable")
	}
	if err := coordinator.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if allowed.Contains(parsed) {
		t.Fatal("stale address remained authorized after status failure")
	}
	if reachable.Contains(parsed) {
		t.Fatal("stale address remained reachable after status failure")
	}
}

func TestKnownOfflinePeerDoesNotKeepWaiting(t *testing.T) {
	coordinator := newTestCoordinator(t, nil, testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
		return nil, nil
	}))
	if err := coordinator.registry.Record(KnownPeer{
		DeviceID:    "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		DisplayName: "Known Offline Peer",
		Addresses:   []string{"100.64.0.2"},
		LastSeenAt:  time.Unix(1, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.store.PublishLocal(LocalUpdate{Text: "local"}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := coordinator.SyncState(); got != SyncUpToDate {
		t.Fatalf("sync state = %q, want %q", got, SyncUpToDate)
	}
}

func TestRegistryWriteFailureDoesNotHideReachableSource(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]
	a.publish(t, "remote")
	b.poll(t)
	b.coordinator.registry = NewRegistry(filepath.Join(t.TempDir(), "missing", "peers.json"))
	b.store.SetSourceReachable(a.deviceID, false)

	b.poll(t)

	if _, err := b.store.ConnectorSnapshot(); err != nil {
		t.Fatal(err)
	}
}

func TestPeerHTTPClientDisablesProxyAndRedirects(t *testing.T) {
	client := newPeerHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("peer transport inherited proxy configuration")
	}
	request, err := http.NewRequest(http.MethodGet, "http://100.64.0.2/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect error = %v", err)
	}
}

func TestCoordinatorOverridesInjectedRedirectPolicy(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unused")
	})
	injected := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return nil
		},
	}
	coordinator := newTestCoordinator(t, injected, testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
		return nil, nil
	}))
	request, err := http.NewRequest(http.MethodGet, "http://100.64.0.2/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.client.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect error = %v", err)
	}
	if _, ok := coordinator.client.Transport.(roundTripFunc); !ok {
		t.Fatalf("transport type = %T", coordinator.client.Transport)
	}
}

func TestCoordinatorRemovesProxyFromInjectedHTTPTransport(t *testing.T) {
	injectedTransport := http.DefaultTransport.(*http.Transport).Clone()
	injectedTransport.Proxy = http.ProxyFromEnvironment
	injected := &http.Client{Transport: injectedTransport}
	coordinator := newTestCoordinator(t, injected, testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
		return nil, nil
	}))
	secured, ok := coordinator.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", coordinator.client.Transport)
	}
	if secured == injectedTransport {
		t.Fatal("injected HTTP transport was not cloned")
	}
	if secured.Proxy != nil {
		t.Fatal("injected proxy configuration was retained")
	}
}

func TestOversizedAssetIsRejectedBeforeBodyRead(t *testing.T) {
	data := []byte("asset")
	asset := AssetManifest{Digest: sha256Hex(data), MIMEType: "image/png", Width: 1, Height: 1, ByteSize: len(data)}
	manifest := ContextManifest{
		ProtocolVersion: ProtocolVersion,
		Revision:        Revision{WallMillis: 1, DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},
		SourceDeviceID:  "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		UpdatedAt:       time.UnixMilli(1).UTC(),
		Assets:          []AssetManifest{asset},
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	body := &readFlagBody{}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == peerContextRoute {
			return testHTTPResponse(request, http.StatusOK, string(manifestJSON)), nil
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          body,
			ContentLength: MaxAssetBytes + 1,
			Request:       request,
		}, nil
	})}
	coordinator := newTestCoordinator(t, client, testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
		return nil, nil
	}))
	if _, _, err := coordinator.fetchSnapshot(context.Background(), probedPeer{baseURL: "http://100.64.0.2:38421"}); err == nil {
		t.Fatal("oversized asset fetch succeeded")
	}
	if body.read.Load() {
		t.Fatal("oversized asset body was read")
	}
}

func TestOversizedManifestIsRejectedBeforeBodyRead(t *testing.T) {
	body := &readFlagBody{}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          body,
			ContentLength: MaxContextJSONBytes + 1,
			Request:       request,
		}, nil
	})}
	coordinator := newTestCoordinator(t, client, testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
		return nil, nil
	}))
	if _, _, err := coordinator.fetchSnapshot(context.Background(), probedPeer{baseURL: "http://100.64.0.2:38421"}); err == nil {
		t.Fatal("oversized manifest fetch succeeded")
	}
	if body.read.Load() {
		t.Fatal("oversized manifest body was read")
	}
}

func TestFetchSnapshotAcceptsMaximumEscapeHeavyManifestExactly(t *testing.T) {
	text := strings.Repeat("\x01", MaxTextBytes)
	want := ContextManifest{
		ProtocolVersion: ProtocolVersion,
		Revision:        Revision{WallMillis: 1, DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},
		SourceDeviceID:  "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		UpdatedAt:       time.UnixMilli(1).UTC(),
		Text:            text,
		Assets:          []AssetManifest{},
	}
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= MaxTextBytes+MaxAssets*512+16<<10 {
		t.Fatalf("escape-heavy manifest size = %d, test does not cross old peer bound", len(body))
	}
	if len(body) > MaxTextBytes*6+(64<<10) {
		t.Fatalf("escape-heavy manifest size = %d, exceeds sound wire bound", len(body))
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return testHTTPResponse(request, http.StatusOK, string(body)), nil
	})}
	coordinator := newTestCoordinator(t, client, testStatusSource(func(context.Context) ([]TailnetCandidate, error) {
		return nil, nil
	}))

	got, assets, err := coordinator.fetchSnapshot(context.Background(), probedPeer{baseURL: "http://100.64.0.2:38421"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != text || got.Revision != want.Revision || got.SourceDeviceID != want.SourceDeviceID {
		t.Fatalf("fetched manifest did not preserve exact maximum text: len=%d", len(got.Text))
	}
	if len(assets) != 0 {
		t.Fatalf("assets = %d, want 0", len(assets))
	}
}

func TestInvalidFetchedManifestDoesNotLeaveUpdatingState(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]
	b.publishWithoutAnnounce(t, "compatible")
	a.publishWithoutAnnounce(t, "older")
	a.publishWithoutAnnounce(t, "invalid")
	a.contextProto.Store(ProtocolVersion + 1)

	if err := b.coordinator.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := b.manifest(t).Text; got != "compatible" {
		t.Fatalf("text = %q", got)
	}
	if got := b.coordinator.SyncState(); got == SyncUpdating {
		t.Fatalf("sync state remained %q", got)
	}
}

func TestFailedFirstSyncStaysWaitingWithoutLocalContext(t *testing.T) {
	cluster := newRuntimeCluster(t, 2)
	a, b := cluster[0], cluster[1]
	b.publishWithoutAnnounce(t, "unfetchable-first-context")
	b.contextProto.Store(ProtocolVersion + 1)

	a.poll(t)

	if got := a.coordinator.SyncState(); got != SyncWaiting {
		t.Fatalf("sync state = %q, want %q", got, SyncWaiting)
	}
	if _, err := a.store.ConnectorSnapshot(); !errors.Is(err, ErrNoContext) {
		t.Fatalf("ConnectorSnapshot() error = %v, want %v", err, ErrNoContext)
	}
}

type readFlagBody struct {
	read atomic.Bool
}

func (body *readFlagBody) Read([]byte) (int, error) {
	body.read.Store(true)
	return 0, errors.New("body must not be read")
}

func (*readFlagBody) Close() error { return nil }

func newTestCoordinator(t *testing.T, client *http.Client, source StatusSource) *Coordinator {
	t.Helper()
	return newTestCoordinatorWithAllowed(t, client, source, &AllowedPeerIPs{})
}

func newTestCoordinatorWithAllowed(t *testing.T, client *http.Client, source StatusSource, allowed *AllowedPeerIPs) *Coordinator {
	t.Helper()
	return newTestCoordinatorWithPeerSets(t, client, source, allowed, &AllowedPeerIPs{})
}

func newTestCoordinatorWithPeerSets(t *testing.T, client *http.Client, source StatusSource, allowed, reachable *AllowedPeerIPs) *Coordinator {
	t.Helper()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	store, err := NewStore("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(CoordinatorOptions{
		DeviceID:       "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		Port:           testPeerPort,
		Store:          store,
		Registry:       NewRegistry(filepath.Join(t.TempDir(), "peers.json")),
		AllowedPeers:   allowed,
		ReachablePeers: reachable,
		Tailscale:      source,
		Now:            func() time.Time { return now },
		HTTPClient:     client,
	})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func testHTTPResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func updateMaximum(maximum *atomic.Int64, candidate int64) {
	for {
		current := maximum.Load()
		if candidate <= current || maximum.CompareAndSwap(current, candidate) {
			return
		}
	}
}

func waitForSignals(t *testing.T, signals <-chan struct{}, count int) {
	t.Helper()
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	for index := 0; index < count; index++ {
		select {
		case <-signals:
		case <-timer.C:
			t.Fatalf("received %d of %d synchronization signals", index, count)
		}
	}
}
