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
		if args[0] == "setup" {
			return runSetup(ctx, args[1:])
		}
		return runProxy(ctx, args)
	}
	return runProxy(ctx, nil)
}

func runProxy(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("mcpaste", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	endpoint := flags.String("endpoint", "", "MCP endpoint")
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
	if *endpoint != "" {
		credential.Endpoint = normalizeMCPEndpoint(*endpoint)
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
	endpoint := flags.String("endpoint", "", "MCPaste service endpoint")
	name := flags.String("name", "linux-companion", "device display name")
	credentialPath := flags.String("credential-file", "", "credential file")
	codexPath := flags.String("codex-config", "", "Codex configuration path")
	claudePath := flags.String("claude-config", "", "Claude Code configuration path")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *endpoint == "" || *name == "" {
		return errors.New("setup requires --endpoint and a non-empty --name")
	}
	apiEndpoint := strings.TrimRight(*endpoint, "/")
	mcpEndpoint := normalizeMCPEndpoint(apiEndpoint)
	if err := connector.ValidateEndpoint(mcpEndpoint); err != nil {
		return err
	}
	apiBase := strings.TrimSuffix(mcpEndpoint, "/v1/mcp")
	apiClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("MCPaste endpoint redirects are not allowed")
	}}
	idempotencyKey, err := secure.NewUUID(secure.SystemRandom{})
	if err != nil {
		return errors.New("create pairing request")
	}
	input, err := json.Marshal(map[string]string{"proposed_name": *name, "platform": "linux", "requested_scope": "connector"})
	if err != nil {
		return errors.New("create pairing request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/v1/pairing-requests", bytes.NewReader(input))
	if err != nil {
		return errors.New("create pairing request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response, err := apiClient.Do(request)
	if err != nil {
		return errors.New("create pairing request")
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusCreated {
		return errors.New("create pairing request")
	}
	var pairing identity.PairingCreateResponse
	if err := json.Unmarshal(body, &pairing); err != nil || pairing.PairingID == "" || pairing.ClaimSecret == "" {
		return errors.New("invalid pairing response")
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
	path := *credentialPath
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
		CommandPath: commandPath, Endpoint: mcpEndpoint, CodexPath: *codexPath, ClaudePath: *claudePath,
	}); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(os.Stdout, "mcpaste connector configured")
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

func normalizeMCPEndpoint(endpoint string) string {
	endpoint = strings.TrimRight(endpoint, "/")
	if strings.HasSuffix(endpoint, "/v1/mcp") {
		return endpoint
	}
	return endpoint + "/v1/mcp"
}
