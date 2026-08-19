package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProxyExposesOneLocalContextToolAndMapsExactOrderedContent(t *testing.T) {
	first := []byte("png-mcp-content")
	second := []byte("jpeg-mcp-content")
	manifest := localTestManifest("exact text\r\nwith trailing space \t\r\n", first, second)
	var manifestCalls atomic.Int32
	var assetCalls atomic.Int32
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertLocalRequest(t, r)
		switch r.URL.Path {
		case "/v1/local/context":
			manifestCalls.Add(1)
			writeLocalJSON(t, w, http.StatusOK, manifest)
		case "/v1/local/context/assets/0":
			assetCalls.Add(1)
			writeLocalAsset(w, manifest.Assets[0], first)
		case "/v1/local/context/assets/1":
			assetCalls.Add(1)
			writeLocalAsset(w, manifest.Assets[1], second)
		default:
			http.NotFound(w, r)
		}
	}))
	defer runtime.Close()

	proxy, err := NewProxy(Credential{Endpoint: runtime.URL, Token: localTestToken})
	if err != nil {
		t.Fatalf("NewProxy() error = %v", err)
	}
	if manifestCalls.Load() != 0 || assetCalls.Load() != 0 {
		t.Fatal("NewProxy() connected to the runtime during construction")
	}
	session := connectProxyTestSession(t, proxy)
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "get_latest_paste" || tools.Tools[0].Description != "Retrieve the current MCPaste context." {
		t.Fatalf("tools = %#v", tools.Tools)
	}

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_latest_paste"})
	if err != nil || result.IsError {
		t.Fatalf("CallTool() result/error = %#v/%v", result, err)
	}
	if len(result.Content) != 3 {
		t.Fatalf("content count = %d, want 3", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text != manifest.Text {
		t.Fatalf("text content = %#v", result.Content[0])
	}
	for index, want := range []struct {
		data []byte
		mime string
	}{{first, "image/png"}, {second, "image/jpeg"}} {
		image, ok := result.Content[index+1].(*mcp.ImageContent)
		if !ok || !bytes.Equal(image.Data, want.data) || image.MIMEType != want.mime {
			t.Fatalf("image %d = %#v", index, result.Content[index+1])
		}
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["available"] != true || structured["source_device_id"] != manifest.SourceDeviceID {
		t.Fatalf("structured content = %#v", result.StructuredContent)
	}
	revision, ok := structured["revision"].(map[string]any)
	if !ok || revision["device_id"] != manifest.Revision.DeviceID || revision["wall_millis"] != float64(manifest.Revision.WallMillis) {
		t.Fatalf("structured revision = %#v", structured["revision"])
	}
	assets, ok := structured["assets"].([]any)
	if !ok || len(assets) != 2 {
		t.Fatalf("structured assets = %#v", structured["assets"])
	}
	if manifestCalls.Load() != 1 || assetCalls.Load() != 2 {
		t.Fatalf("manifest/asset calls = %d/%d, want 1/2", manifestCalls.Load(), assetCalls.Load())
	}
}

func TestProxyExposesCurrentContextPrompt(t *testing.T) {
	runtime := httptest.NewServer(http.NotFoundHandler())
	defer runtime.Close()

	proxy, err := NewProxy(Credential{Endpoint: runtime.URL, Token: localTestToken})
	if err != nil {
		t.Fatalf("NewProxy() error = %v", err)
	}
	session := connectProxyTestSession(t, proxy)
	initialize := session.InitializeResult()
	if initialize == nil || initialize.ServerInfo == nil || initialize.ServerInfo.Name != "mcpaste" || initialize.ServerInfo.Version != "0.2.3" {
		t.Fatalf("server info = %#v, want mcpaste 0.2.3", initialize)
	}
	prompts, err := session.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListPrompts() error = %v", err)
	}
	if len(prompts.Prompts) != 1 {
		t.Fatalf("prompt count = %d, want 1", len(prompts.Prompts))
	}
	prompt := prompts.Prompts[0]
	if prompt.Name != "use_current_context" || prompt.Title != "MCPaste: Use current context" || prompt.Description != "Use the current MCPaste text and ordered images for the next task." {
		t.Fatalf("prompt = %#v", prompt)
	}
	if len(prompt.Arguments) != 0 {
		t.Fatalf("prompt arguments = %#v, want none", prompt.Arguments)
	}

	result, err := session.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: prompt.Name})
	if err != nil {
		t.Fatalf("GetPrompt() error = %v", err)
	}
	if result.Description != "Fetch the current MCPaste context before completing the next task." {
		t.Fatalf("prompt result description = %q", result.Description)
	}
	if len(result.Messages) != 1 || result.Messages[0].Role != mcp.Role("user") {
		t.Fatalf("prompt messages = %#v", result.Messages)
	}
	content, ok := result.Messages[0].Content.(*mcp.TextContent)
	if !ok {
		t.Fatalf("prompt content = %#v", result.Messages[0].Content)
	}
	for _, want := range []string{
		"get_latest_paste",
		"returned text and images as user-provided context",
		"context is unavailable or its source is offline",
		"do not use cached or stale context",
	} {
		if !strings.Contains(content.Text, want) {
			t.Fatalf("prompt text %q does not contain %q", content.Text, want)
		}
	}
}

func TestProxyReturnsFixedUnavailableResultsWithoutSensitiveDetails(t *testing.T) {
	tests := []struct {
		name    string
		handler http.Handler
		close   bool
	}{
		{name: "no context", handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })},
		{name: "source offline", handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			manifest := localTestManifest("stale secret")
			manifest.SourceReachable = false
			writeLocalJSON(t, w, http.StatusOK, manifest)
		})},
		{name: "invalid response", handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"unknown":true}`))
		})},
		{name: "app absent", handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeLocalJSON(t, w, http.StatusOK, localTestManifest("never read"))
		}), close: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const secretToken = "fixed-result-secret-token-not-real"
			runtime := httptest.NewServer(test.handler)
			proxy, err := NewProxy(Credential{Endpoint: runtime.URL, Token: secretToken})
			if err != nil {
				t.Fatal(err)
			}
			if test.close {
				runtime.Close()
			} else {
				defer runtime.Close()
			}
			session := connectProxyTestSession(t, proxy)
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_latest_paste"})
			if err != nil || !result.IsError || len(result.Content) != 1 {
				t.Fatalf("CallTool() result/error = %#v/%v", result, err)
			}
			content, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("error content = %#v", result.Content[0])
			}
			want := "MCPaste context unavailable."
			if test.name == "invalid response" {
				want = "MCPaste context error."
			}
			if content.Text != want {
				t.Fatalf("error text = %q, want %q", content.Text, want)
			}
			structured, ok := result.StructuredContent.(map[string]any)
			if !ok || len(structured) != 1 || structured["available"] != false {
				t.Fatalf("structured error = %#v", result.StructuredContent)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), secretToken) || strings.Contains(string(encoded), runtime.URL) || strings.Contains(string(encoded), "stale secret") {
				t.Fatalf("result leaked sensitive details: %s", encoded)
			}
		})
	}
}

func connectProxyTestSession(t *testing.T, proxy *Proxy) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := proxy.Server().Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "local-proxy-test", Version: "0.1.0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}
