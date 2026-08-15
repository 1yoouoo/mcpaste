package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/1yoouoo/mcpaste/internal/connector"
)

func TestSetupRejectsEndpointFlagBeforeNetwork(t *testing.T) {
	original := connector.BuildEndpoint
	t.Cleanup(func() { connector.BuildEndpoint = original })
	connector.BuildEndpoint = "https://example.test"

	err := runSetup(context.Background(), []string{"--endpoint", "https://other.test", "--name", "test-companion"})
	if err == nil || !strings.Contains(err.Error(), "invalid setup arguments") {
		t.Fatalf("runSetup() error = %v", err)
	}
}

func TestProxyRejectsCredentialEndpointMismatchBeforeConnecting(t *testing.T) {
	original := connector.BuildEndpoint
	t.Cleanup(func() { connector.BuildEndpoint = original })
	connector.BuildEndpoint = "https://example.test"

	path := filepath.Join(t.TempDir(), "credential.json")
	if err := connector.SaveCredential(path, connector.Credential{Endpoint: "https://other.test/v1/mcp", Token: "example-token-not-real"}); err != nil {
		t.Fatalf("SaveCredential() error = %v", err)
	}
	err := runProxy(context.Background(), []string{"--credential-file", path})
	if err == nil || !strings.Contains(err.Error(), "configured endpoint") {
		t.Fatalf("runProxy() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("credential was removed: %v", err)
	}
}
