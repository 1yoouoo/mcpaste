package secure

import (
	"bytes"
	"strings"
	"testing"
)

func TestEnvelopeRoundTripAndNonceUniqueness(t *testing.T) {
	key := bytes.Repeat([]byte{0x31}, 32)
	nonces := bytes.Repeat([]byte{0x41}, 24)
	copy(nonces[12:], bytes.Repeat([]byte{0x42}, 12))
	keyring, err := NewKeyring("test-key", map[string][]byte{"test-key": key}, bytes.NewReader(nonces))
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	first, err := keyring.Encrypt("idempotency", "object-1", []byte("sensitive-marker"))
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	second, err := keyring.Encrypt("idempotency", "object-1", []byte("sensitive-marker"))
	if err != nil {
		t.Fatalf("Encrypt() second error = %v", err)
	}
	if bytes.Equal(first.Nonce, second.Nonce) {
		t.Fatal("nonces are equal")
	}
	plaintext, err := keyring.Decrypt("idempotency", "object-1", first)
	if err != nil || string(plaintext) != "sensitive-marker" {
		t.Fatalf("Decrypt() = %q, %v", plaintext, err)
	}
}

func TestEnvelopeRejectsTamperWrongContextAndUnknownKey(t *testing.T) {
	keyring, err := NewKeyring(
		"test-key",
		map[string][]byte{
			"test-key":  bytes.Repeat([]byte{0x51}, 32),
			"other-key": bytes.Repeat([]byte{0x52}, 32),
		},
		bytes.NewReader(bytes.Repeat([]byte{0x61}, 36)),
	)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	envelope, err := keyring.Encrypt("pairing-grant", "pair-1", []byte("grant-marker"))
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	tampered := envelope
	tampered.Ciphertext = bytes.Clone(envelope.Ciphertext)
	tampered.Ciphertext[0] ^= 0xff
	if _, err := keyring.Decrypt("pairing-grant", "pair-1", tampered); err == nil {
		t.Fatal("tampered ciphertext decrypted")
	}
	if _, err := keyring.Decrypt("pairing-grant", "pair-2", envelope); err == nil {
		t.Fatal("wrong associated data decrypted")
	}
	knownIDTamper := envelope
	knownIDTamper.KeyID = "other-key"
	if _, err := keyring.Decrypt("pairing-grant", "pair-1", knownIDTamper); err == nil {
		t.Fatal("known key identifier tamper decrypted")
	}
	unknownIDTamper := envelope
	unknownIDTamper.KeyID = "missing-key"
	if _, err := keyring.Decrypt("pairing-grant", "pair-1", unknownIDTamper); err == nil {
		t.Fatal("unknown key identifier decrypted")
	}
}

func TestEnvelopeAssociatedDataRejectsTupleCollision(t *testing.T) {
	keyring, err := NewKeyring(
		"test-key",
		map[string][]byte{"test-key": bytes.Repeat([]byte{0x63}, 32)},
		bytes.NewReader(bytes.Repeat([]byte{0x64}, 12)),
	)
	if err != nil {
		t.Fatal("NewKeyring failed")
	}
	envelope, err := keyring.Encrypt("a:b", "c", []byte("tuple-marker"))
	if err != nil {
		t.Fatal("Encrypt failed")
	}
	if _, err := keyring.Decrypt("a", "b:c", envelope); err == nil {
		t.Fatal("colliding purpose and object tuple decrypted")
	}
}

func TestParseKeyringRejectsInvalidConfiguration(t *testing.T) {
	canonical := strings.Repeat("A", 43)
	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "bad identifier", value: "bad id:YWJj"},
		{name: "invalid base64", value: "key:not-base64"},
		{name: "short key", value: "key:YWJj"},
		{name: "padding", value: "key:" + canonical + "="},
		{name: "noncanonical alias", value: "key:" + strings.Repeat("A", 42) + "B"},
		{name: "carriage return", value: "key:" + canonical[:21] + "\r" + canonical[21:]},
		{name: "line feed", value: "key:" + canonical[:21] + "\n" + canonical[21:]},
		{name: "duplicate identifier", value: "key:" + canonical + ",key:" + canonical},
		{name: "duplicate material", value: "key:" + canonical + ",other:" + canonical},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			if _, err := ParseKeyring("key", item.value, bytes.NewReader(nil)); err == nil {
				t.Fatal("ParseKeyring() error = nil")
			}
		})
	}
	if _, err := NewKeyring("first", map[string][]byte{
		"first":  bytes.Repeat([]byte{0x71}, 32),
		"second": bytes.Repeat([]byte{0x71}, 32),
	}, bytes.NewReader(nil)); err == nil {
		t.Fatal("NewKeyring() accepted duplicate key material")
	}
}

func TestEnvelopeRotationRetainsOldKeyAndUsesNewActiveKey(t *testing.T) {
	oldKey := bytes.Repeat([]byte{0x81}, 32)
	newKey := bytes.Repeat([]byte{0x82}, 32)
	oldRing, err := NewKeyring("old-key", map[string][]byte{"old-key": oldKey}, bytes.NewReader(bytes.Repeat([]byte{0x83}, 12)))
	if err != nil {
		t.Fatalf("NewKeyring() old error = %v", err)
	}
	oldEnvelope, err := oldRing.Encrypt("idempotency", "rotation-object", []byte("old-marker"))
	if err != nil {
		t.Fatalf("Encrypt() old error = %v", err)
	}
	rotatedRing, err := NewKeyring(
		"new-key",
		map[string][]byte{"old-key": oldKey, "new-key": newKey},
		bytes.NewReader(bytes.Repeat([]byte{0x84}, 12)),
	)
	if err != nil {
		t.Fatalf("NewKeyring() rotated error = %v", err)
	}
	if plaintext, err := rotatedRing.Decrypt("idempotency", "rotation-object", oldEnvelope); err != nil || string(plaintext) != "old-marker" {
		t.Fatal("rotated keyring could not decrypt retained old envelope")
	}
	newEnvelope, err := rotatedRing.Encrypt("idempotency", "rotation-object", []byte("new-marker"))
	if err != nil {
		t.Fatalf("Encrypt() new error = %v", err)
	}
	if newEnvelope.KeyID != "new-key" {
		t.Fatalf("new envelope key ID = %q", newEnvelope.KeyID)
	}
	if _, err := oldRing.Decrypt("idempotency", "rotation-object", newEnvelope); err == nil {
		t.Fatal("old-only keyring decrypted new envelope")
	}
}
