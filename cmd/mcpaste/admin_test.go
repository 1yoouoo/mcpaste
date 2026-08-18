package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/1yoouoo/mcpaste/internal/connector"
	"github.com/1yoouoo/mcpaste/internal/identity"
)

func TestLoginSavesTheFullCredential(t *testing.T) {
	const fullToken = "example-full-token-not-real"
	var pairingInput map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/pairing-requests" && r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&pairingInput)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(identity.PairingCreateResponse{
				PairingID: "00000000-0000-4000-8000-000000000801",
				ShortCode: "34567891", ClaimSecret: "example-claim-secret-not-real",
			})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/claim") && r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(identity.WorkspaceGrant{Credentials: []identity.CredentialResponse{
				{Kind: "full", Token: fullToken},
				{Kind: "connector", Token: "example-connector-token-not-real"},
			}})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	credentialPath := filepath.Join(t.TempDir(), "admin-credential.json")
	if err := runLoginWithEndpoint(context.Background(), server.URL+"/v1/mcp", "admin-cli", "macos", credentialPath); err != nil {
		t.Fatalf("runLogin() error = %v", err)
	}
	if pairingInput["requested_scope"] != "full" || pairingInput["platform"] != "macos" {
		t.Fatalf("pairing request input = %#v", pairingInput)
	}
	credential, err := connector.LoadCredential(credentialPath)
	if err != nil || credential.Token != fullToken {
		t.Fatalf("saved credential/error = %#v/%v", credential, err)
	}
}

func TestLoginFailsWhenTheGrantHasNoFullCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/pairing-requests" {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(identity.PairingCreateResponse{
				PairingID: "00000000-0000-4000-8000-000000000802",
				ShortCode: "34567892", ClaimSecret: "example-claim-secret-not-real",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(identity.WorkspaceGrant{Credentials: []identity.CredentialResponse{{Kind: "connector", Token: "example-connector-token-not-real"}}})
	}))
	defer server.Close()
	credentialPath := filepath.Join(t.TempDir(), "admin-credential.json")
	err := runLoginWithEndpoint(context.Background(), server.URL+"/v1/mcp", "admin-cli", "macos", credentialPath)
	if err == nil || !strings.Contains(err.Error(), "no full credential") {
		t.Fatalf("runLogin() error = %v", err)
	}
}

func TestApproveLooksUpTheCodeAndApprovesThePairing(t *testing.T) {
	const adminToken = "example-admin-token-not-real"
	var lookupInput map[string]string
	var lookupAuth, approveAuth, approveIdempotency string
	var approvedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/pairing-requests/lookup" && r.Method == http.MethodPost:
			lookupAuth = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&lookupInput)
			_ = json.NewEncoder(w).Encode(identity.PairingDetails{
				PairingID: "00000000-0000-4000-8000-000000000901", ProposedName: "mac-mini connector",
				Platform: "linux", RequestedScope: "connector", Status: "pending",
			})
		case strings.HasSuffix(r.URL.Path, "/approve") && r.Method == http.MethodPost:
			approvedPath = r.URL.Path
			approveAuth = r.Header.Get("Authorization")
			approveIdempotency = r.Header.Get("Idempotency-Key")
			_ = json.NewEncoder(w).Encode(identity.ApprovalResponse{PairingID: "00000000-0000-4000-8000-000000000901", Status: "approved"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	original := connector.BuildEndpoint
	t.Cleanup(func() { connector.BuildEndpoint = original })
	connector.BuildEndpoint = server.URL
	credentialPath := filepath.Join(t.TempDir(), "admin-credential.json")
	if err := connector.SaveCredential(credentialPath, connector.Credential{Endpoint: server.URL + "/v1/mcp", Token: adminToken}); err != nil {
		t.Fatalf("SaveCredential() error = %v", err)
	}
	if err := runApprove(context.Background(), []string{"--credential-file", credentialPath, "34567893"}); err != nil {
		t.Fatalf("runApprove() error = %v", err)
	}
	if lookupInput["short_code"] != "34567893" || lookupAuth != "Bearer "+adminToken {
		t.Fatalf("lookup input/auth = %#v/%q", lookupInput, lookupAuth)
	}
	if approvedPath != "/v1/pairing-requests/00000000-0000-4000-8000-000000000901/approve" {
		t.Fatalf("approve path = %q", approvedPath)
	}
	if approveAuth != "Bearer "+adminToken || approveIdempotency == "" {
		t.Fatalf("approve auth/idempotency = %q/%q", approveAuth, approveIdempotency)
	}
}

func TestApproveRefusesARequestThatIsNoLongerPending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/pairing-requests/lookup" {
			_ = json.NewEncoder(w).Encode(identity.PairingDetails{
				PairingID: "00000000-0000-4000-8000-000000000902", Status: "approved",
			})
			return
		}
		t.Errorf("unexpected request to %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer server.Close()
	original := connector.BuildEndpoint
	t.Cleanup(func() { connector.BuildEndpoint = original })
	connector.BuildEndpoint = server.URL
	credentialPath := filepath.Join(t.TempDir(), "admin-credential.json")
	if err := connector.SaveCredential(credentialPath, connector.Credential{Endpoint: server.URL + "/v1/mcp", Token: "example-admin-token-not-real"}); err != nil {
		t.Fatalf("SaveCredential() error = %v", err)
	}
	err := runApprove(context.Background(), []string{"--credential-file", credentialPath, "34567894"})
	if err == nil || !strings.Contains(err.Error(), "already approved") {
		t.Fatalf("runApprove() error = %v", err)
	}
}

func TestApproveExplainsAnUnknownCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()
	original := connector.BuildEndpoint
	t.Cleanup(func() { connector.BuildEndpoint = original })
	connector.BuildEndpoint = server.URL
	credentialPath := filepath.Join(t.TempDir(), "admin-credential.json")
	if err := connector.SaveCredential(credentialPath, connector.Credential{Endpoint: server.URL + "/v1/mcp", Token: "example-admin-token-not-real"}); err != nil {
		t.Fatalf("SaveCredential() error = %v", err)
	}
	err := runApprove(context.Background(), []string{"--credential-file", credentialPath, "00000000"})
	if err == nil || !strings.Contains(err.Error(), "no pairing request matches") {
		t.Fatalf("runApprove() error = %v", err)
	}
}

func TestApproveRequiresExactlyOneCode(t *testing.T) {
	for _, args := range [][]string{{}, {"a", "b"}} {
		err := runApprove(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "invalid approve arguments") {
			t.Fatalf("runApprove(%v) error = %v", args, err)
		}
	}
}

func TestApproveWithoutACredentialPointsAtLogin(t *testing.T) {
	credentialPath := filepath.Join(t.TempDir(), "missing.json")
	err := runApprove(context.Background(), []string{"--credential-file", credentialPath, "34567895"})
	if err == nil || !strings.Contains(err.Error(), "mcpaste login") {
		t.Fatalf("runApprove() error = %v", err)
	}
}
