package connector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/1yoouoo/mcpaste/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type proxyLatestService struct{}

func (proxyLatestService) LatestPaste(context.Context, identity.Principal) (identity.LatestPaste, error) {
	return identity.LatestPaste{Available: true, Text: "proxy exact text\r\n끝"}, nil
}

func TestProxyForwardsOnlyLatestPasteWithBearerHeader(t *testing.T) {
	const token = "example-token-not-real"
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "wrong auth", http.StatusUnauthorized)
			return
		}
		if r.URL.RawQuery != "" || r.URL.String() != "/v1/mcp" {
			http.Error(w, "credential in URL", http.StatusBadRequest)
			return
		}
		mcpserver.NewHandler(proxyLatestService{}).ServeHTTP(w, r.WithContext(mcpserver.WithPrincipal(r.Context(), identity.Principal{Scope: "connector"})))
	}))
	defer remote.Close()
	proxy, err := NewProxy(context.Background(), Credential{Endpoint: remote.URL + "/v1/mcp", Token: token}, remote.Client())
	if err != nil {
		t.Fatalf("NewProxy() error = %v", err)
	}
	defer proxy.Close()
	local := httptest.NewServer(proxy.Handler())
	defer local.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "proxy-test", Version: "0.1.0"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: local.URL, MaxRetries: -1, DisableStandaloneSSE: true}
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("local Connect() error = %v", err)
	}
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "get_latest_paste" {
		t.Fatalf("proxy tools/error = %#v/%v", tools, err)
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_latest_paste"})
	if err != nil || result.IsError || len(result.Content) != 1 {
		t.Fatalf("proxy result/error = %#v/%v", result, err)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text != "proxy exact text\r\n끝" {
		t.Fatalf("proxy text = %#v", result.Content[0])
	}
}
