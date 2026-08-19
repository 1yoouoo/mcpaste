package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

const (
	peerRequestTimeout  = 750 * time.Millisecond
	maxPeerHealthBody   = 16 << 10
	maxPeerManifestBody = MaxContextJSONBytes
)

var ErrInvalidCoordinator = errors.New("invalid peer coordinator")

type StatusSource interface {
	Status(context.Context) ([]TailnetCandidate, error)
}

type CoordinatorOptions struct {
	DeviceID       string
	Port           int
	Store          *Store
	Registry       *Registry
	AllowedPeers   *AllowedPeerIPs
	ReachablePeers *AllowedPeerIPs
	Tailscale      StatusSource
	Now            func() time.Time
	HTTPClient     *http.Client
}

type Coordinator struct {
	deviceID       string
	port           int
	store          *Store
	registry       *Registry
	allowedPeers   *AllowedPeerIPs
	reachablePeers *AllowedPeerIPs
	tailscale      StatusSource
	now            func() time.Time
	client         *http.Client

	pollMu                sync.Mutex
	stateMu               sync.RWMutex
	state                 SyncState
	pendingPropagation    Revision
	hasPendingPropagation bool
}

type probedPeer struct {
	baseURL string
	address string
	health  healthResponse
}

type probeResult struct {
	index int
	peer  probedPeer
	ok    bool
}

type assetResult struct {
	index int
	data  []byte
	err   error
}

func NewCoordinator(options CoordinatorOptions) (*Coordinator, error) {
	if options.DeviceID == "" || options.Port < 1 || options.Port > 65535 || options.Store == nil ||
		options.Registry == nil || options.AllowedPeers == nil || options.ReachablePeers == nil || options.Tailscale == nil || options.Now == nil {
		return nil, ErrInvalidCoordinator
	}
	client := securePeerHTTPClient(options.HTTPClient)
	return &Coordinator{
		deviceID:       options.DeviceID,
		port:           options.Port,
		store:          options.Store,
		registry:       options.Registry,
		allowedPeers:   options.AllowedPeers,
		reachablePeers: options.ReachablePeers,
		tailscale:      options.Tailscale,
		now:            options.Now,
		client:         client,
		state:          SyncUpToDate,
	}, nil
}

func newPeerHTTPClient() *http.Client {
	return securePeerHTTPClient(nil)
}

func securePeerHTTPClient(client *http.Client) *http.Client {
	secured := http.Client{}
	if client != nil {
		secured = *client
	}
	if secured.Transport == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		secured.Transport = transport
	} else if transport, ok := secured.Transport.(*http.Transport); ok {
		transport = transport.Clone()
		transport.Proxy = nil
		secured.Transport = transport
	}
	secured.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &secured
}

func (coordinator *Coordinator) SyncState() SyncState {
	if coordinator == nil {
		return SyncWaiting
	}
	coordinator.stateMu.RLock()
	defer coordinator.stateMu.RUnlock()
	return coordinator.state
}

func (coordinator *Coordinator) setState(state SyncState) {
	coordinator.stateMu.Lock()
	coordinator.state = state
	coordinator.stateMu.Unlock()
}

func (coordinator *Coordinator) PollOnce(ctx context.Context) error {
	if coordinator == nil || ctx == nil {
		return ErrInvalidCoordinator
	}
	coordinator.pollMu.Lock()
	defer coordinator.pollMu.Unlock()

	candidates, err := coordinator.tailscale.Status(ctx)
	if err != nil {
		coordinator.allowedPeers.Replace(nil)
		coordinator.reachablePeers.Replace(nil)
		coordinator.markSourceReachable(nil)
		manifest, manifestErr := coordinator.store.Manifest()
		coordinator.finishUnavailableState(manifest, manifestErr)
		return nil
	}
	coordinator.replaceAllowedPeers(candidates)

	peers := coordinator.probePeers(ctx, candidates)
	coordinator.replaceReachablePeers(peers)
	coordinator.markSourceReachable(peers)

	local, localErr := coordinator.store.Manifest()
	if localErr != nil && !errors.Is(localErr, ErrNoContext) {
		return localErr
	}
	winner := local
	hasWinner := localErr == nil
	var source *probedPeer
	for index := range peers {
		peer := &peers[index]
		if !peer.health.HasContext || (hasWinner && peer.health.Revision.Compare(winner.Revision) <= 0) {
			continue
		}
		winner = ContextManifest{Revision: peer.health.Revision}
		hasWinner = true
		source = peer
	}

	adopted := false
	if source != nil {
		coordinator.setState(SyncUpdating)
		manifest, assets, fetchErr := coordinator.fetchSnapshot(ctx, *source)
		if fetchErr == nil {
			before, beforeErr := coordinator.store.Manifest()
			if err := coordinator.store.AdoptRemote(manifest, assets); err != nil {
				coordinator.finishState(peers, winner.Revision)
				return nil
			}
			after, afterErr := coordinator.store.Manifest()
			adopted = afterErr == nil && (beforeErr != nil || after.Revision.Compare(before.Revision) > 0)
			winner = after
		} else {
			coordinator.finishState(peers, winner.Revision)
			return nil
		}
		coordinator.markSourceReachable(peers)
	}

	if hasWinner && (adopted || winner.SourceDeviceID == coordinator.deviceID) {
		coordinator.finishState(peers, winner.Revision)
		coordinator.announceOlder(ctx, peers, winner.Revision)
	}
	coordinator.finishState(peers, winner.Revision)
	return nil
}

func (coordinator *Coordinator) HandleAnnouncement(ctx context.Context, revision Revision) error {
	if err := coordinator.PollOnce(ctx); err != nil {
		return err
	}
	manifest, err := coordinator.store.Manifest()
	if err != nil || manifest.Revision.Compare(revision) < 0 {
		return errors.New("announced revision unavailable")
	}
	return nil
}

func (coordinator *Coordinator) replaceAllowedPeers(candidates []TailnetCandidate) {
	addresses := make([]netip.Addr, 0)
	for _, candidate := range candidates {
		for _, raw := range candidate.Addresses {
			address, err := netip.ParseAddr(raw)
			if err == nil && address.IsValid() && address.Zone() == "" {
				addresses = append(addresses, address)
			}
		}
	}
	coordinator.allowedPeers.Replace(addresses)
}

func (coordinator *Coordinator) replaceReachablePeers(peers []probedPeer) {
	addresses := make([]netip.Addr, 0, len(peers))
	for _, peer := range peers {
		address, err := netip.ParseAddr(peer.address)
		if err == nil && address.IsValid() && address.Zone() == "" {
			addresses = append(addresses, address)
		}
	}
	coordinator.reachablePeers.Replace(addresses)
}

func (coordinator *Coordinator) probePeers(ctx context.Context, candidates []TailnetCandidate) []probedPeer {
	if len(candidates) == 0 {
		return nil
	}
	jobs := make(chan int, len(candidates))
	results := make(chan probeResult, len(candidates))
	for index := range candidates {
		jobs <- index
	}
	close(jobs)
	workers := 4
	if len(candidates) < workers {
		workers = len(candidates)
	}
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for index := range jobs {
				peer, ok := coordinator.probeCandidate(ctx, candidates[index])
				results <- probeResult{index: index, peer: peer, ok: ok}
			}
		}()
	}
	wait.Wait()
	close(results)

	ordered := make([]probeResult, len(candidates))
	for result := range results {
		ordered[result.index] = result
	}
	peers := make([]probedPeer, 0, len(candidates))
	for _, result := range ordered {
		if !result.ok {
			continue
		}
		peer := result.peer
		peers = append(peers, peer)
		_ = coordinator.registry.Record(KnownPeer{
			DeviceID:    peer.health.DeviceID,
			DisplayName: peer.health.DisplayName,
			Addresses:   []string{peer.address},
			LastSeenAt:  coordinator.now(),
		})
	}
	return peers
}

func (coordinator *Coordinator) probeCandidate(ctx context.Context, candidate TailnetCandidate) (probedPeer, bool) {
	for _, address := range candidate.Addresses {
		peer, err := coordinator.probePeer(ctx, address)
		if err == nil {
			return peer, true
		}
	}
	return probedPeer{}, false
}

func (coordinator *Coordinator) probePeer(ctx context.Context, address string) (probedPeer, error) {
	baseURL := "http://" + net.JoinHostPort(address, strconv.Itoa(coordinator.port))
	var health healthResponse
	if err := coordinator.getJSON(ctx, baseURL+"/v1/health", maxPeerHealthBody, &health); err != nil {
		return probedPeer{}, err
	}
	deviceID, validDevice := normalizeDeviceID(health.DeviceID)
	if health.ProtocolVersion != ProtocolVersion || !validDevice || !validDisplayName(health.DisplayName) {
		return probedPeer{}, errors.New("invalid peer identity")
	}
	health.DeviceID = deviceID
	if health.HasContext {
		sourceID, validSource := normalizeDeviceID(health.SourceDeviceID)
		revisionID, validRevision := normalizeDeviceID(health.Revision.DeviceID)
		if !validSource || !validRevision || sourceID != revisionID || health.Revision.WallMillis == math.MaxInt64 ||
			revisionTooFarAhead(health.Revision.WallMillis, coordinator.now().UnixMilli()) {
			return probedPeer{}, errors.New("invalid peer context identity")
		}
		health.SourceDeviceID = sourceID
		health.Revision.DeviceID = revisionID
	}
	return probedPeer{baseURL: baseURL, address: address, health: health}, nil
}

func (coordinator *Coordinator) fetchSnapshot(ctx context.Context, source probedPeer) (ContextManifest, map[string][]byte, error) {
	var manifest ContextManifest
	if err := coordinator.getJSON(ctx, source.baseURL+peerContextRoute, maxPeerManifestBody, &manifest); err != nil {
		return ContextManifest{}, nil, err
	}
	if len(manifest.Assets) > MaxAssets {
		return ContextManifest{}, nil, ErrLimitExceeded
	}
	for _, asset := range manifest.Assets {
		if asset.ByteSize < 0 || asset.ByteSize > MaxAssetBytes {
			return ContextManifest{}, nil, ErrLimitExceeded
		}
	}
	assets := make(map[string][]byte, len(manifest.Assets))
	if len(manifest.Assets) == 0 {
		return manifest, assets, nil
	}
	fetchContext, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int, len(manifest.Assets))
	results := make(chan assetResult, len(manifest.Assets))
	for index := range manifest.Assets {
		jobs <- index
	}
	close(jobs)
	workers := 3
	if len(manifest.Assets) < workers {
		workers = len(manifest.Assets)
	}
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for index := range jobs {
				data, err := coordinator.getBytes(fetchContext, source.baseURL+peerContextAssetsBase+strconv.Itoa(index), int64(manifest.Assets[index].ByteSize))
				results <- assetResult{index: index, data: data, err: err}
				if err != nil {
					cancel()
				}
			}
		}()
	}
	wait.Wait()
	close(results)
	var firstErr error
	ordered := make([][]byte, len(manifest.Assets))
	for result := range results {
		if result.err != nil && firstErr == nil {
			firstErr = result.err
		}
		ordered[result.index] = result.data
	}
	if firstErr != nil {
		return ContextManifest{}, nil, firstErr
	}
	for index, asset := range manifest.Assets {
		assets[asset.Digest] = ordered[index]
	}
	return manifest, assets, nil
}

func (coordinator *Coordinator) getJSON(ctx context.Context, endpoint string, limit int64, destination any) error {
	requestContext, cancel := context.WithTimeout(ctx, peerRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := coordinator.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("peer request failed")
	}
	if limit < 0 || response.ContentLength > limit {
		return ErrLimitExceeded
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return errors.New("invalid peer response")
	}
	if int64(len(data)) > limit {
		return ErrLimitExceeded
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("invalid peer response")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("invalid peer response")
	}
	return nil
}

func (coordinator *Coordinator) getBytes(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	requestContext, cancel := context.WithTimeout(ctx, peerRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := coordinator.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("peer request failed")
	}
	if limit < 0 || limit > MaxAssetBytes || response.ContentLength > limit {
		return nil, ErrLimitExceeded
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrLimitExceeded
	}
	return data, nil
}

func (coordinator *Coordinator) announceOlder(ctx context.Context, peers []probedPeer, revision Revision) {
	body, err := json.Marshal(announceEnvelope{Revision: revision})
	if err != nil {
		return
	}
	for _, peer := range peers {
		if peer.health.HasContext && peer.health.Revision.Compare(revision) >= 0 {
			continue
		}
		requestContext, cancel := context.WithTimeout(ctx, peerRequestTimeout)
		request, err := http.NewRequestWithContext(requestContext, http.MethodPost, peer.baseURL+"/v1/announce", bytes.NewReader(body))
		if err == nil {
			request.Header.Set("Content-Type", "application/json")
			response, requestErr := coordinator.client.Do(request)
			if requestErr == nil {
				if response.StatusCode == http.StatusNoContent {
					coordinator.recordPendingPropagation(revision)
				}
				_ = response.Body.Close()
			}
		}
		cancel()
	}
}

func (coordinator *Coordinator) markSourceReachable(peers []probedPeer) {
	manifest, err := coordinator.store.Manifest()
	if err != nil {
		return
	}
	reachable := manifest.SourceDeviceID == coordinator.deviceID
	if !reachable {
		for _, peer := range peers {
			if peer.health.DeviceID == manifest.SourceDeviceID {
				reachable = true
				break
			}
		}
	}
	coordinator.store.SetSourceReachable(manifest.SourceDeviceID, reachable)
}

func (coordinator *Coordinator) finishUnavailableState(manifest ContextManifest, manifestErr error) {
	coordinator.stateMu.Lock()
	defer coordinator.stateMu.Unlock()
	coordinator.clearPendingForStoreWinnerLocked(manifest, manifestErr)
	if manifestErr == nil && manifest.SourceDeviceID != coordinator.deviceID {
		coordinator.state = SyncSourceOffline
		return
	}
	coordinator.state = SyncWaiting
}

func (coordinator *Coordinator) recordPendingPropagation(revision Revision) {
	coordinator.stateMu.Lock()
	coordinator.pendingPropagation = revision
	coordinator.hasPendingPropagation = true
	coordinator.state = SyncUpdating
	coordinator.stateMu.Unlock()
}

func (coordinator *Coordinator) clearPendingForStoreWinnerLocked(manifest ContextManifest, manifestErr error) {
	if coordinator.hasPendingPropagation &&
		(manifestErr != nil || coordinator.pendingPropagation.Compare(manifest.Revision) != 0) {
		coordinator.pendingPropagation = Revision{}
		coordinator.hasPendingPropagation = false
	}
}

func (coordinator *Coordinator) finishState(peers []probedPeer, winner Revision) {
	manifest, err := coordinator.store.Manifest()
	sourceOffline := false
	if err == nil {
		_, snapshotErr := coordinator.store.ConnectorSnapshot()
		sourceOffline = errors.Is(snapshotErr, ErrSourceOffline)
	}

	coordinator.stateMu.Lock()
	defer coordinator.stateMu.Unlock()
	coordinator.clearPendingForStoreWinnerLocked(manifest, err)

	if errors.Is(err, ErrNoContext) {
		if winner == (Revision{}) {
			coordinator.state = SyncUpToDate
		} else {
			coordinator.state = SyncWaiting
		}
		return
	}
	if err == nil {
		if winner == (Revision{}) {
			winner = manifest.Revision
		}
	}
	pendingCurrent := err == nil && coordinator.hasPendingPropagation &&
		coordinator.pendingPropagation.Compare(manifest.Revision) == 0
	if pendingCurrent {
		pendingConfirmed := true
		for _, peer := range peers {
			if !peer.health.HasContext || peer.health.Revision.Compare(manifest.Revision) != 0 {
				pendingConfirmed = false
				break
			}
		}
		if pendingConfirmed {
			coordinator.pendingPropagation = Revision{}
			coordinator.hasPendingPropagation = false
			pendingCurrent = false
		}
	}
	if sourceOffline {
		coordinator.state = SyncSourceOffline
		return
	}
	if pendingCurrent {
		coordinator.state = SyncUpdating
		return
	}
	if err == nil && manifest.Revision.Compare(winner) != 0 {
		coordinator.state = SyncWaiting
		return
	}
	confirmed := true
	for _, peer := range peers {
		if !peer.health.HasContext || peer.health.Revision.Compare(winner) != 0 {
			confirmed = false
			break
		}
	}
	if confirmed {
		coordinator.state = SyncUpToDate
		return
	}
	coordinator.state = SyncWaiting
}
