package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/internal/connector"
	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/1yoouoo/mcpaste/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type processLatestPaste struct {
	mu        sync.Mutex
	available bool
	text      string
}

func (s *processLatestPaste) LatestPaste(context.Context, identity.Principal) (identity.LatestPaste, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return identity.LatestPaste{Available: s.available, Text: s.text}, nil
}

func TestRealStdioProcessRetrievesExactRemoteMCPTextAndEmptyResult(t *testing.T) {
	const token = "example-token-not-real"
	latest := &processLatestPaste{available: true, text: "process exact\r\n끝  "}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token || r.URL.RawQuery != "" || r.URL.Path != "/v1/mcp" {
			http.Error(w, "invalid remote request", http.StatusUnauthorized)
			return
		}
		mcpserver.NewHandler(latest).ServeHTTP(w, r.WithContext(mcpserver.WithPrincipal(r.Context(), identity.Principal{Scope: "connector"})))
	}))
	defer remote.Close()
	configHome := t.TempDir()
	if err := connector.SaveCredential(filepath.Join(configHome, "mcpaste", "credential.json"), connector.Credential{Endpoint: remote.URL + "/v1/mcp", Token: token}); err != nil {
		t.Fatalf("SaveCredential() error = %v", err)
	}
	binary := filepath.Join(t.TempDir(), "mcpaste")
	build := exec.Command("go", "build", "-o", binary, ".")
	if _, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mcpaste process: %v", err)
	}
	command := exec.Command(binary)
	command.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configHome)
	var stderr strings.Builder
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-e2e-client", Version: "0.1.0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatalf("connect to real STDIO process: %v", err)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "get_latest_paste" {
		t.Fatalf("STDIO tools/error = %#v/%v", tools, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_latest_paste"})
	if err != nil || result.IsError || len(result.Content) != 1 {
		t.Fatalf("STDIO text result/error = %#v/%v", result, err)
	}
	textContent, ok := result.Content[0].(*mcp.TextContent)
	if !ok || textContent.Text != latest.text {
		t.Fatalf("STDIO text content = %#v", result.Content[0])
	}
	latest.mu.Lock()
	latest.available = false
	latest.mu.Unlock()
	empty, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_latest_paste"})
	if err != nil || empty.IsError || len(empty.Content) != 0 {
		t.Fatalf("STDIO empty result/error = %#v/%v", empty, err)
	}
	metadata, ok := empty.StructuredContent.(map[string]any)
	if !ok || metadata["available"] != false {
		t.Fatalf("STDIO empty metadata = %#v", empty.StructuredContent)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close STDIO session: %v", err)
	}
	if strings.Contains(stderr.String(), token) {
		t.Fatal("STDIO stderr contains credential")
	}
}
