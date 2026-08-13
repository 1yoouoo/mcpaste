package connector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialFileIsMode0600AndAtomicallyReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "mcpaste", "credential.json")
	first := Credential{Endpoint: "https://example.invalid/v1/mcp", Token: "example-token-not-real"}
	if err := SaveCredential(path, first); err != nil {
		t.Fatalf("SaveCredential() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credential: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %o, want 600", info.Mode().Perm())
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat credential directory: %v", err)
	}
	if directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("credential directory mode = %o, want 700", directoryInfo.Mode().Perm())
	}
	second := Credential{Endpoint: "http://127.0.0.1:1/v1/mcp", Token: "example-token-replacement-not-real"}
	if err := SaveCredential(path, second); err != nil {
		t.Fatalf("replacement SaveCredential() error = %v", err)
	}
	loaded, err := LoadCredential(path)
	if err != nil {
		t.Fatalf("LoadCredential() error = %v", err)
	}
	if loaded != second {
		t.Fatalf("loaded credential = %#v, want %#v", loaded, second)
	}
}

func TestCredentialErrorsDoNotEchoToken(t *testing.T) {
	secret := "example-token-not-real"
	err := SaveCredential(filepath.Join(t.TempDir(), "credential.json"), Credential{Endpoint: "https://example.invalid", Token: ""})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("credential error = %v", err)
	}
}
