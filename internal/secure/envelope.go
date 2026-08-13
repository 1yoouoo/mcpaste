package secure

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"strings"
)

const nonceSize = 12

type Envelope struct {
	KeyID      string
	Nonce      []byte
	Ciphertext []byte
}

type Keyring struct {
	active string
	keys   map[string][]byte
	random Random
}

func ParseKeyring(active, encoded string, random Random) (*Keyring, error) {
	if encoded == "" {
		return nil, errors.New("encryption keyring is empty")
	}
	keys := make(map[string][]byte)
	for _, item := range strings.Split(encoded, ",") {
		parts := strings.SplitN(item, ":", 2)
		if len(parts) != 2 || !validKeyID(parts[0]) {
			return nil, errors.New("encryption keyring entry is invalid")
		}
		if _, exists := keys[parts[0]]; exists {
			return nil, errors.New("encryption key identifier is duplicated")
		}
		decoded, err := decodeCanonicalRawURL(parts[1], 32)
		if err != nil {
			return nil, errors.New("encryption key must be 32 raw URL-base64 bytes")
		}
		for _, existing := range keys {
			if bytes.Equal(existing, decoded) {
				return nil, errors.New("encryption key material is duplicated")
			}
		}
		keys[parts[0]] = decoded
	}
	return NewKeyring(active, keys, random)
}

func NewKeyring(active string, keys map[string][]byte, random Random) (*Keyring, error) {
	if !validKeyID(active) || random == nil {
		return nil, errors.New("active key identifier or random source is invalid")
	}
	copied := make(map[string][]byte, len(keys))
	for id, key := range keys {
		if !validKeyID(id) || len(key) != 32 {
			return nil, errors.New("keyring contains an invalid key")
		}
		for _, existing := range copied {
			if bytes.Equal(existing, key) {
				return nil, errors.New("keyring contains duplicate key material")
			}
		}
		copied[id] = bytes.Clone(key)
	}
	if _, ok := copied[active]; !ok {
		return nil, errors.New("active key identifier is absent from keyring")
	}
	return &Keyring{active: active, keys: copied, random: random}, nil
}

func (k *Keyring) Encrypt(purpose, objectID string, plaintext []byte) (Envelope, error) {
	aead, err := newGCM(k.keys[k.active])
	if err != nil {
		return Envelope{}, err
	}
	nonce, err := randomBytes(k.random, nonceSize)
	if err != nil {
		return Envelope{}, fmt.Errorf("generate envelope nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, associatedData(k.active, purpose, objectID))
	return Envelope{KeyID: k.active, Nonce: nonce, Ciphertext: ciphertext}, nil
}

func (k *Keyring) Decrypt(purpose, objectID string, envelope Envelope) ([]byte, error) {
	key, ok := k.keys[envelope.KeyID]
	if !ok || len(envelope.Nonce) != nonceSize {
		return nil, errors.New("encrypted envelope is invalid")
	}
	aead, err := newGCM(key)
	if err != nil {
		return nil, errors.New("encrypted envelope is invalid")
	}
	plaintext, err := aead.Open(nil, envelope.Nonce, envelope.Ciphertext, associatedData(envelope.KeyID, purpose, objectID))
	if err != nil {
		return nil, errors.New("encrypted envelope authentication failed")
	}
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func associatedData(keyID, purpose, objectID string) []byte {
	return []byte("mcpaste:v1:" + keyID + ":" + purpose + ":" + objectID)
}

func validKeyID(value string) bool {
	if len(value) < 1 || len(value) > 32 {
		return false
	}
	for index, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (index > 0 && (r == '_' || r == '-')) {
			continue
		}
		return false
	}
	return true
}
