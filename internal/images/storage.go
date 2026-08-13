package images

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/1yoouoo/mcpaste/internal/secure"
)

type StoredAsset struct {
	StorageKey string
	Envelope   secure.Envelope
}

type FileStore struct {
	root    string
	keyring *secure.Keyring
}

func NewFileStore(root string, keyring *secure.Keyring) (*FileStore, error) {
	if root == "" || filepath.IsAbs(root) == false {
		return nil, errors.New("image data directory must be an absolute path")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, errors.New("create image data directory")
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("invalid image data directory")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, errors.New("secure image data directory")
	}
	return &FileStore{root: filepath.Clean(root), keyring: keyring}, nil
}

func (s *FileStore) Put(workspaceID, pasteID, revisionID string, index int, plaintext []byte) (StoredAsset, error) {
	if err := validateIdentifiers(workspaceID, pasteID, revisionID, index); err != nil || s.keyring == nil {
		return StoredAsset{}, ErrInvalidImage
	}
	objectID := fmt.Sprintf("%s:%s:%s:%d", workspaceID, pasteID, revisionID, index)
	envelope, err := s.keyring.Encrypt("paste-image", objectID, plaintext)
	if err != nil {
		return StoredAsset{}, errors.New("encrypt image")
	}
	key := filepath.Join(workspaceID, pasteID, revisionID, fmt.Sprintf("asset-%02d.bin", index))
	path := filepath.Join(s.root, key)
	if !isWithin(s.root, path) {
		return StoredAsset{}, ErrInvalidImage
	}
	if err := s.ensureDirectories(filepath.Dir(path)); err != nil {
		return StoredAsset{}, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".asset-*")
	if err != nil {
		return StoredAsset{}, errors.New("create image temporary file")
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return StoredAsset{}, errors.New("secure image temporary file")
	}
	if _, err := temporary.Write(envelope.Ciphertext); err != nil {
		_ = temporary.Close()
		return StoredAsset{}, errors.New("write image")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return StoredAsset{}, errors.New("sync image")
	}
	if err := temporary.Close(); err != nil {
		return StoredAsset{}, errors.New("close image")
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return StoredAsset{}, errors.New("publish image")
	}
	return StoredAsset{StorageKey: key, Envelope: envelope}, nil
}

func (s *FileStore) ensureDirectories(path string) error {
	relative, err := filepath.Rel(s.root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return ErrInvalidImage
	}
	current := s.root
	for _, part := range strings.Split(relative, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return errors.New("create image storage path")
			}
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("invalid image storage path")
		}
		if err := os.Chmod(current, 0o700); err != nil {
			return errors.New("secure image storage path")
		}
	}
	return nil
}

func (s *FileStore) Open(asset StoredAsset) ([]byte, error) {
	if asset.StorageKey == "" || filepath.IsAbs(asset.StorageKey) || !isWithin(s.root, filepath.Join(s.root, asset.StorageKey)) || s.keyring == nil {
		return nil, ErrInvalidImage
	}
	path := filepath.Join(s.root, asset.StorageKey)
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, ErrUnavailable
	}
	ciphertext, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	plain, err := s.keyring.Decrypt("paste-image", objectIDFromKey(asset.StorageKey), secure.Envelope{KeyID: asset.Envelope.KeyID, Nonce: asset.Envelope.Nonce, Ciphertext: ciphertext})
	if err != nil {
		return nil, ErrUnavailable
	}
	return plain, nil
}

func (s *FileStore) Remove(asset StoredAsset) error {
	if asset.StorageKey == "" || filepath.IsAbs(asset.StorageKey) || !isWithin(s.root, filepath.Join(s.root, asset.StorageKey)) {
		return ErrInvalidImage
	}
	if err := os.Remove(filepath.Join(s.root, asset.StorageKey)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New("remove image")
	}
	return nil
}

func (s *FileStore) RemoveTree(workspaceID, pasteID, revisionID string) error {
	if err := validateIdentifiers(workspaceID, pasteID, revisionID, 0); err != nil {
		return err
	}
	path := filepath.Join(s.root, workspaceID, pasteID, revisionID)
	if !isWithin(s.root, path) {
		return ErrInvalidImage
	}
	if err := os.RemoveAll(path); err != nil {
		return errors.New("remove image revision")
	}
	return nil
}

func (s *FileStore) RemovePaste(workspaceID, pasteID string) error {
	if !validUUID(workspaceID) || !validUUID(pasteID) {
		return ErrInvalidImage
	}
	path := filepath.Join(s.root, workspaceID, pasteID)
	if !isWithin(s.root, path) {
		return ErrInvalidImage
	}
	if err := os.RemoveAll(path); err != nil {
		return errors.New("remove paste images")
	}
	return nil
}

var ErrUnavailable = errors.New("image unavailable")

func isWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !((len(rel) > 3) && rel[:3] == ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func objectIDFromKey(key string) string {
	parts := filepath.ToSlash(key)
	parts = filepath.Clean(parts)
	segments := splitPath(parts)
	if len(segments) != 4 {
		return "invalid"
	}
	var index string
	if len(segments[3]) < 10 {
		return "invalid"
	}
	index = segments[3][6 : len(segments[3])-4]
	parsed, err := strconv.Atoi(index)
	if err != nil {
		return "invalid"
	}
	return segments[0] + ":" + segments[1] + ":" + segments[2] + ":" + strconv.Itoa(parsed)
}

func splitPath(value string) []string {
	result := make([]string, 0, 4)
	for value != "" && value != "." && value != string(os.PathSeparator) {
		part := filepath.Base(value)
		result = append([]string{part}, result...)
		next := filepath.Dir(value)
		if next == value {
			break
		}
		value = next
	}
	return result
}
