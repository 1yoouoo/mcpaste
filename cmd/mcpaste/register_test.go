package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/1yoouoo/mcpaste/internal/connector"
)

func TestRegisterConfiguresClientsWithExistingCredential(t *testing.T) {
	directory := t.TempDir()
	credentialPath := filepath.Join(directory, "credential.json")
	codexPath := filepath.Join(directory, "codex.toml")
	claudePath := filepath.Join(directory, "claude.json")
	credential := connector.Credential{Endpoint: "https://example.invalid/v1/mcp", Token: "example-token-not-real"}
	if err := connector.SaveCredential(credentialPath, credential); err != nil {
		t.Fatalf("SaveCredential() error = %v", err)
	}
	err := runRegister([]string{
		"--credential-file", credentialPath,
		"--codex-config", codexPath,
		"--claude-config", claudePath,
	})
	if err != nil {
		t.Fatalf("runRegister() error = %v", err)
	}
	for _, path := range []string{codexPath, claudePath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read configured client %s: %v", path, err)
		}
		if strings.Contains(string(data), credential.Token) {
			t.Fatalf("client config contains credential: %s", data)
		}
	}
}

func TestRegisterFailsWithoutCredential(t *testing.T) {
	directory := t.TempDir()
	err := runRegister([]string{
		"--credential-file", filepath.Join(directory, "missing.json"),
		"--codex-config", filepath.Join(directory, "codex.toml"),
	})
	if err == nil || !strings.Contains(err.Error(), "no connector credential") {
		t.Fatalf("runRegister() error = %v, want missing-credential error", err)
	}
}

func TestRegisterRejectsPositionalArguments(t *testing.T) {
	if err := runRegister([]string{"extra"}); err == nil || err.Error() != "invalid register arguments" {
		t.Fatalf("runRegister() error = %v, want invalid register arguments", err)
	}
}
