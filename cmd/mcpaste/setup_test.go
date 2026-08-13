package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/1yoouoo/mcpaste/internal/connector"
	"github.com/1yoouoo/mcpaste/internal/identity"
)

func TestSetupSavesConnectorAndConfiguresClientsWithoutTokenInConfig(t *testing.T) {
	const token = "example-token-not-real"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/pairing-requests" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(identity.PairingCreateResponse{
				PairingID: "00000000-0000-4000-8000-000000000701", QRPayload: "mcpaste://pair/00000000-0000-4000-8000-000000000701",
				ShortCode: "23456789", ClaimSecret: "example-claim-secret-not-real",
			})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/claim") && r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(identity.WorkspaceGrant{Credentials: []identity.CredentialResponse{{Kind: "connector", Token: token}}})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	directory := t.TempDir()
	credentialPath := filepath.Join(directory, "credential.json")
	codexPath := filepath.Join(directory, "codex.toml")
	claudePath := filepath.Join(directory, "claude.json")
	if err := runSetup(context.Background(), []string{
		"--endpoint", server.URL, "--name", "test-companion", "--credential-file", credentialPath,
		"--codex-config", codexPath, "--claude-config", claudePath,
	}); err != nil {
		t.Fatalf("runSetup() error = %v", err)
	}
	credential, err := connector.LoadCredential(credentialPath)
	if err != nil || credential.Token != token {
		t.Fatalf("saved credential/error = %#v/%v", credential, err)
	}
	for _, path := range []string{codexPath, claudePath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read configured client %s: %v", path, err)
		}
		if strings.Contains(string(data), token) || !strings.Contains(string(data), "--endpoint") {
			t.Fatalf("client config contains credential or lacks endpoint: %s", data)
		}
	}
}
