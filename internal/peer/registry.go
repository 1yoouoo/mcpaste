package peer

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxRegistryBytes    = 1 << 20
	maxDisplayNameBytes = 255
	registryFileMode    = 0600
)

var (
	ErrInvalidRegistry     = errors.New("invalid peer registry")
	ErrRegistryUnavailable = errors.New("peer registry unavailable")
)

type KnownPeer struct {
	DeviceID    string    `json:"device_id"`
	DisplayName string    `json:"display_name"`
	Addresses   []string  `json:"addresses"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

type Registry struct {
	mu    sync.RWMutex
	path  string
	peers map[string]KnownPeer
}

func NewRegistry(path string) *Registry {
	return &Registry{
		path:  path,
		peers: make(map[string]KnownPeer),
	}
}

func (r *Registry) Record(peer KnownPeer) error {
	normalized, err := normalizeKnownPeer(peer)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.path == "" {
		return ErrRegistryUnavailable
	}
	next := clonePeerMap(r.peers)
	next[normalized.DeviceID] = normalized
	if err := r.persist(next); err != nil {
		return err
	}
	r.peers = next
	return nil
}

func (r *Registry) List() []KnownPeer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return orderedPeers(r.peers)
}

func (r *Registry) Load() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.path == "" {
		return ErrRegistryUnavailable
	}

	peers, err := readRegistry(r.path)
	if err != nil {
		return err
	}
	r.peers = peers
	return nil
}

func normalizeKnownPeer(peer KnownPeer) (KnownPeer, error) {
	deviceID, ok := normalizeDeviceID(peer.DeviceID)
	if !ok || !validDisplayName(peer.DisplayName) || peer.LastSeenAt.IsZero() {
		return KnownPeer{}, ErrInvalidRegistry
	}
	addresses, err := canonicalRegistryAddresses(peer.Addresses)
	if err != nil {
		return KnownPeer{}, ErrInvalidRegistry
	}
	return KnownPeer{
		DeviceID:    deviceID,
		DisplayName: peer.DisplayName,
		Addresses:   addresses,
		LastSeenAt:  peer.LastSeenAt.UTC(),
	}, nil
}

func normalizeDeviceID(value string) (string, bool) {
	if len(value) != 36 {
		return "", false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return "", false
			}
			continue
		}
		if !isHex(character) {
			return "", false
		}
	}
	normalized := strings.ToLower(value)
	if normalized == "00000000-0000-0000-0000-000000000000" {
		return "", false
	}
	return normalized, true
}

func isHex(character rune) bool {
	return character >= '0' && character <= '9' ||
		character >= 'a' && character <= 'f' ||
		character >= 'A' && character <= 'F'
}

func validDisplayName(value string) bool {
	if value == "" || strings.TrimSpace(value) == "" || len(value) > maxDisplayNameBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	return true
}

func canonicalRegistryAddresses(addresses []string) ([]string, error) {
	if len(addresses) == 0 {
		return nil, ErrInvalidRegistry
	}
	seen := make(map[string]struct{}, len(addresses))
	canonical := make([]string, 0, len(addresses))
	for _, raw := range addresses {
		address, err := netip.ParseAddr(raw)
		if err != nil || !address.IsValid() || address.Zone() != "" {
			return nil, ErrInvalidRegistry
		}
		value := address.String()
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		canonical = append(canonical, value)
	}
	sort.Strings(canonical)
	return canonical, nil
}

func (r *Registry) persist(peers map[string]KnownPeer) error {
	if info, err := os.Lstat(r.path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidRegistry
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrRegistryUnavailable
	}

	data, err := json.Marshal(orderedPeers(peers))
	if err != nil || len(data)+1 > maxRegistryBytes {
		if len(data)+1 > maxRegistryBytes {
			return ErrInvalidRegistry
		}
		return ErrRegistryUnavailable
	}
	data = append(data, '\n')

	directory := filepath.Dir(r.path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(r.path)+".tmp-*")
	if err != nil {
		return ErrRegistryUnavailable
	}
	temporaryName := temporary.Name()
	closed := false
	removeTemporary := true
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()

	if err := temporary.Chmod(registryFileMode); err != nil {
		return ErrRegistryUnavailable
	}
	if _, err := temporary.Write(data); err != nil {
		return ErrRegistryUnavailable
	}
	if err := temporary.Sync(); err != nil {
		return ErrRegistryUnavailable
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return ErrRegistryUnavailable
	}
	closed = true

	if info, err := os.Lstat(r.path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidRegistry
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrRegistryUnavailable
	}
	if err := commitRegistryFile(temporaryName, r.path); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

func commitRegistryFile(temporaryName, target string) error {
	if err := os.Rename(temporaryName, target); err != nil {
		return ErrRegistryUnavailable
	}
	bestEffortSyncDirectory(filepath.Dir(target))
	return nil
}

func bestEffortSyncDirectory(directory string) {
	directoryFile, err := os.Open(directory)
	if err != nil {
		return
	}
	_ = directoryFile.Sync()
	_ = directoryFile.Close()
}

func readRegistry(path string) (map[string]KnownPeer, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]KnownPeer), nil
	}
	if err != nil {
		return nil, ErrRegistryUnavailable
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidRegistry
	}
	if !info.Mode().IsRegular() || info.Size() > maxRegistryBytes {
		return nil, ErrInvalidRegistry
	}

	file, err := openRegistryNoFollow(path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]KnownPeer), nil
	}
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxRegistryBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, ErrRegistryUnavailable
	}
	if len(data) > maxRegistryBytes {
		return nil, ErrInvalidRegistry
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, ErrInvalidRegistry
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var records []KnownPeer
	if err := decoder.Decode(&records); err != nil {
		return nil, ErrInvalidRegistry
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, ErrInvalidRegistry
	}

	peers := make(map[string]KnownPeer, len(records))
	for _, record := range records {
		normalized, err := normalizeKnownPeer(record)
		if err != nil {
			return nil, ErrInvalidRegistry
		}
		if _, exists := peers[normalized.DeviceID]; exists {
			return nil, ErrInvalidRegistry
		}
		peers[normalized.DeviceID] = normalized
	}
	return peers, nil
}

func openRegistryNoFollow(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, ErrInvalidRegistry
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, ErrRegistryUnavailable
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, ErrRegistryUnavailable
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, ErrInvalidRegistry
	}
	if info.Size() > maxRegistryBytes {
		_ = file.Close()
		return nil, ErrInvalidRegistry
	}
	return file, nil
}

func clonePeerMap(peers map[string]KnownPeer) map[string]KnownPeer {
	cloned := make(map[string]KnownPeer, len(peers)+1)
	for deviceID, peer := range peers {
		cloned[deviceID] = cloneKnownPeer(peer)
	}
	return cloned
}

func orderedPeers(peers map[string]KnownPeer) []KnownPeer {
	ordered := make([]KnownPeer, 0, len(peers))
	for _, peer := range peers {
		ordered = append(ordered, cloneKnownPeer(peer))
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].DeviceID < ordered[j].DeviceID
	})
	return ordered
}

func cloneKnownPeer(peer KnownPeer) KnownPeer {
	peer.Addresses = append([]string(nil), peer.Addresses...)
	return peer
}
