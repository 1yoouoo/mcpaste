package peer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"sync"
	"time"
)

var (
	ErrNoContext          = errors.New("no current context")
	ErrSourceOffline      = errors.New("current source offline")
	ErrInvalidAsset       = errors.New("invalid context asset")
	ErrLimitExceeded      = errors.New("store limit exceeded")
	ErrInvalidStoreConfig = errors.New("invalid store configuration")
	ErrProtocolMismatch   = errors.New("protocol version mismatch")
	ErrInvalidRevision    = errors.New("invalid context revision")
)

type limitExceededError struct{}

func (limitExceededError) Error() string {
	return "store limit exceeded: invalid context asset"
}

func (limitExceededError) Unwrap() []error {
	return []error{ErrLimitExceeded, ErrInvalidAsset}
}

var errLimitExceeded error = limitExceededError{}

type LocalUpdate struct {
	Text         string   `json:"text"`
	AssetDigests []string `json:"asset_digests"`
}

type stagedAsset struct {
	manifest AssetManifest
	data     []byte
	stagedAt time.Time
}

type Store struct {
	mu              sync.RWMutex
	clock           *Clock
	localDeviceID   string
	now             func() time.Time
	current         *Snapshot
	staged          map[string]stagedAsset
	stagedBytes     int
	sourceReachable bool
}

func NewStore(deviceID string, now func() time.Time) (store *Store, err error) {
	if deviceID == "" || now == nil {
		return nil, ErrInvalidStoreConfig
	}

	defer func() {
		if recover() != nil {
			store = nil
			err = ErrInvalidStoreConfig
		}
	}()

	return &Store{
		clock:         NewClock(deviceID, now),
		localDeviceID: deviceID,
		now:           now,
		staged:        make(map[string]stagedAsset),
	}, nil
}

func (s *Store) StageAsset(manifest AssetManifest, data []byte) error {
	if manifest.ByteSize > MaxAssetBytes || len(data) > MaxAssetBytes {
		return errLimitExceeded
	}
	if !validAsset(manifest, data) {
		return ErrInvalidAsset
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.staged == nil {
		s.staged = make(map[string]stagedAsset)
	}
	previous, exists := s.staged[manifest.Digest]
	if !exists && len(s.staged) >= MaxAssets {
		return errLimitExceeded
	}
	stagedBytes := s.stagedBytes
	if exists {
		stagedBytes -= previous.manifest.ByteSize
	}
	if stagedBytes > MaxBundleBytes-manifest.ByteSize {
		return errLimitExceeded
	}
	s.stagedBytes = stagedBytes + manifest.ByteSize
	s.staged[manifest.Digest] = stagedAsset{
		manifest: manifest,
		data:     cloneBytes(data),
		stagedAt: s.now(),
	}
	return nil
}

func (s *Store) PublishLocal(update LocalUpdate) (ContextManifest, error) {
	if len(update.Text) > MaxTextBytes || len(update.AssetDigests) > MaxAssets {
		return ContextManifest{}, errLimitExceeded
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	assets := make([]AssetManifest, 0, len(update.AssetDigests))
	assetBytes := make(map[string][]byte, len(update.AssetDigests))
	bundleBytes := len(update.Text)
	for _, digest := range update.AssetDigests {
		asset, data, ok := s.resolveAssetLocked(digest)
		if !ok || !validAsset(asset, data) {
			return ContextManifest{}, ErrInvalidAsset
		}
		if bundleBytes > MaxBundleBytes-asset.ByteSize {
			return ContextManifest{}, errLimitExceeded
		}
		bundleBytes += asset.ByteSize
		assets = append(assets, asset)
		assetBytes[digest] = cloneBytes(data)
	}

	revision, err := s.clock.TryTick()
	if err != nil {
		return ContextManifest{}, err
	}
	manifest := ContextManifest{
		ProtocolVersion: ProtocolVersion,
		Revision:        revision,
		SourceDeviceID:  s.localDeviceID,
		UpdatedAt:       s.now(),
		Text:            update.Text,
		Assets:          assets,
	}
	s.current = &Snapshot{
		Manifest: cloneManifest(manifest),
		Assets:   cloneBytesMap(assetBytes),
	}
	s.sourceReachable = true
	for _, digest := range update.AssetDigests {
		s.removeStagedLocked(digest)
	}
	return manifest, nil
}

func (s *Store) AdoptRemote(manifest ContextManifest, assets map[string][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	candidate, candidateAssets, err := validateRemote(manifest, assets, s.now())
	if err != nil {
		return err
	}
	if s.current != nil && candidate.Revision.Compare(s.current.Manifest.Revision) <= 0 {
		return nil
	}
	if !s.clock.Observe(candidate.Revision) {
		return ErrClockExhausted
	}

	s.current = &Snapshot{
		Manifest: candidate,
		Assets:   candidateAssets,
	}
	s.sourceReachable = true
	return nil
}

func (s *Store) Manifest() (ContextManifest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.current == nil {
		return ContextManifest{}, ErrNoContext
	}
	return cloneManifest(s.current.Manifest), nil
}

func (s *Store) ConnectorSnapshot() (Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.current == nil {
		return Snapshot{}, ErrNoContext
	}
	if !s.sourceReachable {
		return Snapshot{}, ErrSourceOffline
	}
	return Snapshot{
		Manifest: cloneManifest(s.current.Manifest),
		Assets:   cloneBytesMap(s.current.Assets),
	}, nil
}

func (s *Store) SetSourceReachable(sourceDeviceID string, reachable bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current == nil || s.current.Manifest.SourceDeviceID != sourceDeviceID {
		return false
	}
	s.sourceReachable = reachable
	return true
}

func (s *Store) SweepStaged() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.staged) == 0 {
		return
	}
	now := s.now()
	for digest, asset := range s.staged {
		if now.Sub(asset.stagedAt) > 30*time.Second {
			s.removeStagedLocked(digest)
		}
	}
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (s *Store) resolveAssetLocked(digest string) (AssetManifest, []byte, bool) {
	if staged, ok := s.staged[digest]; ok {
		return staged.manifest, staged.data, true
	}
	if s.current == nil {
		return AssetManifest{}, nil, false
	}
	for _, asset := range s.current.Manifest.Assets {
		if asset.Digest == digest {
			data, ok := s.current.Assets[digest]
			return asset, data, ok
		}
	}
	return AssetManifest{}, nil, false
}

func validateRemote(manifest ContextManifest, assets map[string][]byte, now time.Time) (ContextManifest, map[string][]byte, error) {
	if manifest.ProtocolVersion != ProtocolVersion {
		return ContextManifest{}, nil, ErrProtocolMismatch
	}
	if manifest.Revision.DeviceID == "" ||
		manifest.SourceDeviceID == "" ||
		manifest.Revision.DeviceID != manifest.SourceDeviceID ||
		manifest.Revision.WallMillis == math.MaxInt64 ||
		revisionTooFarAhead(manifest.Revision.WallMillis, now.UnixMilli()) {
		return ContextManifest{}, nil, ErrInvalidRevision
	}
	if len(manifest.Text) > MaxTextBytes || len(manifest.Assets) > MaxAssets {
		return ContextManifest{}, nil, errLimitExceeded
	}

	candidate := cloneManifest(manifest)
	candidateAssets := make(map[string][]byte, len(manifest.Assets))
	declared := make(map[string]struct{}, len(manifest.Assets))
	seen := make(map[string]AssetManifest, len(manifest.Assets))
	bundleBytes := len(manifest.Text)
	for _, asset := range manifest.Assets {
		if first, ok := seen[asset.Digest]; ok && first != asset {
			return ContextManifest{}, nil, ErrInvalidAsset
		}
		seen[asset.Digest] = asset
		data, ok := assets[asset.Digest]
		if asset.ByteSize > MaxAssetBytes || len(data) > MaxAssetBytes {
			return ContextManifest{}, nil, errLimitExceeded
		}
		if !ok || !validAsset(asset, data) {
			return ContextManifest{}, nil, ErrInvalidAsset
		}
		if bundleBytes > MaxBundleBytes-asset.ByteSize {
			return ContextManifest{}, nil, errLimitExceeded
		}
		bundleBytes += asset.ByteSize
		declared[asset.Digest] = struct{}{}
		if _, ok := candidateAssets[asset.Digest]; !ok {
			candidateAssets[asset.Digest] = cloneBytes(data)
		}
	}
	if len(assets) != len(declared) {
		return ContextManifest{}, nil, ErrInvalidAsset
	}
	return candidate, candidateAssets, nil
}

const maxFutureRevisionMillis = int64((24 * time.Hour) / time.Millisecond)

func revisionTooFarAhead(wallMillis, nowMillis int64) bool {
	if wallMillis <= nowMillis || nowMillis > math.MaxInt64-maxFutureRevisionMillis {
		return false
	}
	return wallMillis > nowMillis+maxFutureRevisionMillis
}

func (s *Store) removeStagedLocked(digest string) {
	asset, ok := s.staged[digest]
	if !ok {
		return
	}
	delete(s.staged, digest)
	s.stagedBytes -= asset.manifest.ByteSize
}

func validAsset(manifest AssetManifest, data []byte) bool {
	if manifest.MIMEType != "image/png" && manifest.MIMEType != "image/jpeg" {
		return false
	}
	if manifest.Width <= 0 || manifest.Height <= 0 {
		return false
	}
	if manifest.ByteSize != len(data) || manifest.ByteSize < 0 || manifest.ByteSize > MaxAssetBytes {
		return false
	}
	return len(manifest.Digest) == 64 && sha256Hex(data) == manifest.Digest
}

func cloneManifest(manifest ContextManifest) ContextManifest {
	cloned := manifest
	cloned.Assets = make([]AssetManifest, len(manifest.Assets))
	copy(cloned.Assets, manifest.Assets)
	return cloned
}

func cloneBytes(data []byte) []byte {
	cloned := make([]byte, len(data))
	copy(cloned, data)
	return cloned
}

func cloneBytesMap(assets map[string][]byte) map[string][]byte {
	cloned := make(map[string][]byte, len(assets))
	for digest, data := range assets {
		cloned[digest] = cloneBytes(data)
	}
	return cloned
}
