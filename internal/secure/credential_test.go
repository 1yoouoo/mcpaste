package secure

import (
	"bytes"
	"crypto/subtle"
	"strings"
	"testing"
)

const testWorkspaceID = "00000000-0000-4000-8000-000000000001"

func TestCredentialCarriesLocatorAndHashesSecret(t *testing.T) {
	randomInput := make([]byte, 48)
	copy(randomInput[:16], bytes.Repeat([]byte{0x11}, 16))
	copy(randomInput[16:], bytes.Repeat([]byte{0x22}, 32))
	source := bytes.NewReader(randomInput)
	issued, err := NewCredential(testWorkspaceID, "full", source)
	if err != nil {
		t.Fatalf("NewCredential() error = %v", err)
	}
	if len(issued.Locator) != 22 || len(issued.Hash) != 32 || len(issued.Token) != 108 {
		t.Fatalf("issued lengths = locator %d hash %d token %d", len(issued.Locator), len(issued.Hash), len(issued.Token))
	}
	parsed, err := ParseCredential(issued.Token)
	if err != nil {
		t.Fatalf("ParseCredential() error = %v", err)
	}
	if parsed.WorkspaceID != testWorkspaceID || parsed.Locator != issued.Locator || subtle.ConstantTimeCompare(parsed.Hash, issued.Hash) != 1 {
		t.Fatalf("parsed credential metadata differs")
	}
}

func TestCredentialRejectsMalformedValues(t *testing.T) {
	canonicalLocator := strings.Repeat("A", 22)
	canonicalSecret := strings.Repeat("A", 43)
	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "segments", value: "mcp1"},
		{name: "prefix", value: "mcp2.a.b.c"},
		{name: "workspace", value: "mcp1.bad-uuid.a.b"},
		{name: "short segments", value: "mcp1." + testWorkspaceID + ".short.short"},
		{name: "locator padding", value: "mcp1." + testWorkspaceID + "." + canonicalLocator + "=." + canonicalSecret},
		{name: "secret padding", value: "mcp1." + testWorkspaceID + "." + canonicalLocator + "." + canonicalSecret + "="},
		{name: "locator alias", value: "mcp1." + testWorkspaceID + "." + strings.Repeat("A", 21) + "B." + canonicalSecret},
		{name: "secret alias", value: "mcp1." + testWorkspaceID + "." + canonicalLocator + "." + strings.Repeat("A", 42) + "B"},
		{name: "locator carriage return", value: "mcp1." + testWorkspaceID + "." + canonicalLocator[:11] + "\r" + canonicalLocator[11:] + "." + canonicalSecret},
		{name: "locator line feed", value: "mcp1." + testWorkspaceID + "." + canonicalLocator[:11] + "\n" + canonicalLocator[11:] + "." + canonicalSecret},
		{name: "secret carriage return", value: "mcp1." + testWorkspaceID + "." + canonicalLocator + "." + canonicalSecret[:21] + "\r" + canonicalSecret[21:]},
		{name: "secret line feed", value: "mcp1." + testWorkspaceID + "." + canonicalLocator + "." + canonicalSecret[:21] + "\n" + canonicalSecret[21:]},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			if _, err := ParseCredential(item.value); err == nil {
				t.Fatal("ParseCredential() error = nil")
			}
		})
	}
}

func TestClaimSecretUses256BitsAndStableHash(t *testing.T) {
	secret, hash, err := NewClaimSecret(bytes.NewReader(bytes.Repeat([]byte{0x73}, 32)))
	if err != nil {
		t.Fatalf("NewClaimSecret() error = %v", err)
	}
	if len(secret) != 43 || len(hash) != 32 {
		t.Fatalf("secret/hash lengths = %d/%d", len(secret), len(hash))
	}
	parsed, err := HashClaimSecret(secret)
	if err != nil || subtle.ConstantTimeCompare(parsed, hash) != 1 {
		t.Fatalf("HashClaimSecret() mismatch or error = %v", err)
	}
}

func TestClaimSecretRejectsNoncanonicalValues(t *testing.T) {
	tests := map[string]string{
		"padding":         "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"alias":           strings.Repeat("A", 42) + "B",
		"carriage return": strings.Repeat("A", 21) + "\r" + strings.Repeat("A", 22),
		"line feed":       strings.Repeat("A", 21) + "\n" + strings.Repeat("A", 22),
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := HashClaimSecret(value); err == nil {
				t.Fatal("HashClaimSecret() error = nil")
			}
		})
	}
}
