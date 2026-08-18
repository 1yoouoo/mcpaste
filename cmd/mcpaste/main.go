package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/1yoouoo/mcpaste/internal/connector"
	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/1yoouoo/mcpaste/internal/secure"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "mcpaste: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) != 0 {
		switch args[0] {
		case "setup":
			return runSetup(ctx, args[1:])
		case "login":
			return runLogin(ctx, args[1:])
		case "approve":
			return runApprove(ctx, args[1:])
		}
		return runProxy(ctx, args)
	}
	return runProxy(ctx, nil)
}

func runProxy(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("mcpaste", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	credentialPath := flags.String("credential-file", "", "credential file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("invalid proxy arguments")
	}
	path := *credentialPath
	if path == "" {
		var err error
		path, err = connector.DefaultCredentialPath()
		if err != nil {
			return err
		}
	}
	credential, err := connector.LoadCredential(path)
	if err != nil {
		return err
	}
	if err := connector.ValidateConfiguredEndpoint(credential.Endpoint); err != nil {
		return err
	}
	proxy, err := connector.NewProxy(ctx, credential, http.DefaultClient)
	if err != nil {
		return err
	}
	defer proxy.Close()
	return proxy.Server().Run(ctx, &mcp.StdioTransport{})
}

func runSetup(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("mcpaste setup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("name", "linux-companion", "device display name")
	credentialPath := flags.String("credential-file", "", "credential file")
	codexPath := flags.String("codex-config", "", "Codex configuration path")
	claudePath := flags.String("claude-config", "", "Claude Code configuration path")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *name == "" {
		return errors.New("invalid setup arguments")
	}
	mcpEndpoint, err := connector.ConfiguredMCPEndpoint()
	if err != nil {
		return err
	}
	return runSetupWithEndpoint(ctx, mcpEndpoint, *name, *credentialPath, *codexPath, *claudePath)
}

func runSetupWithEndpoint(ctx context.Context, mcpEndpoint, name, credentialPath, codexPath, claudePath string) error {
	if err := connector.ValidateEndpoint(mcpEndpoint); err != nil {
		return err
	}
	apiBase := strings.TrimSuffix(mcpEndpoint, "/v1/mcp")
	pairing, err := createPairingRequest(ctx, apiBase, name, runtimePlatform(), "connector")
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "pairing_id=%s short_code=%s qr_payload=%s\n", pairing.PairingID, pairing.ShortCode, pairing.QRPayload)
	grant, err := claimPairing(ctx, apiBase, pairing)
	if err != nil {
		return err
	}
	token := ""
	for _, item := range grant.Credentials {
		if item.Kind == "connector" {
			token = item.Token
			break
		}
	}
	if token == "" {
		return errors.New("pairing response has no connector credential")
	}
	path := credentialPath
	if path == "" {
		path, err = connector.DefaultCredentialPath()
		if err != nil {
			return err
		}
	}
	if err := connector.SaveCredential(path, connector.Credential{Endpoint: mcpEndpoint, Token: token}); err != nil {
		return err
	}
	commandPath, err := os.Executable()
	if err != nil {
		return errors.New("resolve mcpaste executable")
	}
	if err := connector.ConfigureClients(connector.ClientConfigOptions{
		CommandPath: commandPath, CodexPath: codexPath, ClaudePath: claudePath,
	}); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(os.Stdout, "mcpaste connector configured")
	return nil
}

// runLogin obtains an admin (full-scope) credential for this machine so that
// `mcpaste approve` can work from a terminal. The pairing request still has to
// be approved once from an existing full device, keeping the trust chain intact.
func runLogin(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("mcpaste login", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("name", "admin-cli", "device display name")
	credentialPath := flags.String("credential-file", "", "admin credential file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *name == "" {
		return errors.New("invalid login arguments")
	}
	mcpEndpoint, err := connector.ConfiguredMCPEndpoint()
	if err != nil {
		return err
	}
	return runLoginWithEndpoint(ctx, mcpEndpoint, *name, runtimePlatform(), *credentialPath)
}

func runLoginWithEndpoint(ctx context.Context, mcpEndpoint, name, platform, credentialPath string) error {
	if err := connector.ValidateEndpoint(mcpEndpoint); err != nil {
		return err
	}
	apiBase := strings.TrimSuffix(mcpEndpoint, "/v1/mcp")
	pairing, err := createPairingRequest(ctx, apiBase, name, platform, "full")
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "pairing_id=%s short_code=%s\n", pairing.PairingID, pairing.ShortCode)
	_, _ = fmt.Fprintln(os.Stdout, "approve this code from a full device: MCPaste menu bar > Workspace & devices > Approve a device")
	grant, err := claimPairing(ctx, apiBase, pairing)
	if err != nil {
		return err
	}
	token := ""
	for _, item := range grant.Credentials {
		if item.Kind == "full" {
			token = item.Token
			break
		}
	}
	if token == "" {
		return errors.New("pairing response has no full credential")
	}
	path := credentialPath
	if path == "" {
		path, err = defaultAdminCredentialPath()
		if err != nil {
			return err
		}
	}
	if err := connector.SaveCredential(path, connector.Credential{Endpoint: mcpEndpoint, Token: token}); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "mcpaste admin credential saved to %s\n", path)
	return nil
}

// runApprove approves a pairing request another device printed (e.g. `mcpaste
// setup` on a connector host) using the admin credential from `mcpaste login`.
func runApprove(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("mcpaste approve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	credentialPath := flags.String("credential-file", "", "admin credential file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 || flags.Arg(0) == "" {
		return errors.New("invalid approve arguments (usage: mcpaste approve <short-code>)")
	}
	shortCode := flags.Arg(0)
	path := *credentialPath
	if path == "" {
		var err error
		path, err = defaultAdminCredentialPath()
		if err != nil {
			return err
		}
	}
	credential, err := connector.LoadCredential(path)
	if err != nil {
		return errors.New("no admin credential; run `mcpaste login` first")
	}
	if err := connector.ValidateConfiguredEndpoint(credential.Endpoint); err != nil {
		return err
	}
	apiBase := strings.TrimSuffix(credential.Endpoint, "/v1/mcp")
	details, err := lookupPairingRequest(ctx, apiBase, credential.Token, shortCode)
	if err != nil {
		return err
	}
	if details.Status != "pending" {
		return fmt.Errorf("pairing request is already %s", details.Status)
	}
	if err := approvePairingRequest(ctx, apiBase, credential.Token, details.PairingID); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "approved %s (%s, %s)\n", details.ProposedName, details.Platform, details.RequestedScope)
	return nil
}

// runtimePlatform reports this machine as one of the platforms the pairing API
// accepts; the server only grants full scope to macos.
func runtimePlatform() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return "linux"
}

func defaultAdminCredentialPath() (string, error) {
	path, err := connector.DefaultCredentialPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "admin-credential.json"), nil
}

func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("MCPaste endpoint redirects are not allowed")
	}}
}

func createPairingRequest(ctx context.Context, apiBase, name, platform, scope string) (identity.PairingCreateResponse, error) {
	fail := identity.PairingCreateResponse{}
	idempotencyKey, err := secure.NewUUID(secure.SystemRandom{})
	if err != nil {
		return fail, errors.New("create pairing request")
	}
	input, err := json.Marshal(map[string]string{"proposed_name": name, "platform": platform, "requested_scope": scope})
	if err != nil {
		return fail, errors.New("create pairing request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/v1/pairing-requests", bytes.NewReader(input))
	if err != nil {
		return fail, errors.New("create pairing request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response, err := noRedirectClient().Do(request)
	if err != nil {
		return fail, errors.New("create pairing request")
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusCreated {
		return fail, errors.New("create pairing request")
	}
	var pairing identity.PairingCreateResponse
	if err := json.Unmarshal(body, &pairing); err != nil || pairing.PairingID == "" || pairing.ClaimSecret == "" {
		return fail, errors.New("invalid pairing response")
	}
	return pairing, nil
}

func lookupPairingRequest(ctx context.Context, apiBase, token, shortCode string) (identity.PairingDetails, error) {
	fail := identity.PairingDetails{}
	input, err := json.Marshal(map[string]string{"short_code": shortCode})
	if err != nil {
		return fail, errors.New("look up pairing request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/v1/pairing-requests/lookup", bytes.NewReader(input))
	if err != nil {
		return fail, errors.New("look up pairing request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := noRedirectClient().Do(request)
	if err != nil {
		return fail, errors.New("look up pairing request")
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		return fail, errors.New("look up pairing request")
	}
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return fail, errors.New("no pairing request matches that code")
	case http.StatusUnauthorized, http.StatusForbidden:
		return fail, errors.New("admin credential was rejected; run `mcpaste login` again")
	default:
		return fail, errors.New("look up pairing request")
	}
	var details identity.PairingDetails
	if err := json.Unmarshal(body, &details); err != nil || details.PairingID == "" {
		return fail, errors.New("invalid pairing lookup response")
	}
	return details, nil
}

func approvePairingRequest(ctx context.Context, apiBase, token, pairingID string) error {
	idempotencyKey, err := secure.NewUUID(secure.SystemRandom{})
	if err != nil {
		return errors.New("approve pairing request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/v1/pairing-requests/"+pairingID+"/approve", strings.NewReader("{}"))
	if err != nil {
		return errors.New("approve pairing request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response, err := noRedirectClient().Do(request)
	if err != nil {
		return errors.New("approve pairing request")
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return errors.New("approve pairing request")
	}
	return nil
}

func claimPairing(ctx context.Context, apiBase string, pairing identity.PairingCreateResponse) (identity.WorkspaceGrant, error) {
	deadline := time.Now().Add(5 * time.Minute)
	if !pairing.ExpiresAt.IsZero() && pairing.ExpiresAt.Before(deadline) {
		deadline = pairing.ExpiresAt
	}
	claimContext, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		input, err := json.Marshal(map[string]string{"claim_secret": pairing.ClaimSecret})
		if err != nil {
			return identity.WorkspaceGrant{}, errors.New("claim pairing")
		}
		request, err := http.NewRequestWithContext(claimContext, http.MethodPost, apiBase+"/v1/pairing-requests/"+pairing.PairingID+"/claim", bytes.NewReader(input))
		if err != nil {
			return identity.WorkspaceGrant{}, errors.New("claim pairing")
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("MCPaste endpoint redirects are not allowed")
		}}).Do(request)
		if err != nil {
			return identity.WorkspaceGrant{}, errors.New("claim pairing")
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			return identity.WorkspaceGrant{}, errors.New("claim pairing")
		}
		if response.StatusCode == http.StatusOK {
			var grant identity.WorkspaceGrant
			if err := json.Unmarshal(body, &grant); err != nil {
				return identity.WorkspaceGrant{}, errors.New("invalid pairing grant")
			}
			return grant, nil
		}
		if response.StatusCode != http.StatusConflict {
			return identity.WorkspaceGrant{}, errors.New("claim pairing")
		}
		select {
		case <-claimContext.Done():
			return identity.WorkspaceGrant{}, claimContext.Err()
		case <-ticker.C:
		}
	}
}
