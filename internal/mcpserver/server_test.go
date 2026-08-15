package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeLatestPasteService struct {
	latest identity.LatestPaste
}

func (s *fakeLatestPasteService) LatestPaste(context.Context, identity.Principal) (identity.LatestPaste, error) {
	return s.latest, nil
}

func TestMCPListsOnlyLatestPasteAndPreservesExactText(t *testing.T) {
	exact := "  line one\r\nline two\n끝  "
	service := &fakeLatestPasteService{latest: identity.LatestPaste{
		Available: true, PasteID: "00000000-0000-4000-8000-000000000601", RevisionID: "00000000-0000-4000-8000-000000000602",
		ServerSequence: 42, CreatedAt: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2027, 8, 13, 12, 0, 0, 0, time.UTC), Text: exact,
	}}
	session := connectTestClient(t, service)
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "get_latest_paste" {
		t.Fatalf("tools = %#v", tools.Tools)
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_latest_paste", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("CallTool() result = %#v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text != exact {
		t.Fatalf("text content = %#v", result.Content[0])
	}
	metadata, ok := result.StructuredContent.(map[string]any)
	if !ok || metadata["available"] != true || metadata["server_sequence"] != float64(42) {
		t.Fatalf("structured metadata = %#v", result.StructuredContent)
	}
}

func TestMCPReturnsStructuredEmptyResult(t *testing.T) {
	session := connectTestClient(t, &fakeLatestPasteService{})
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_latest_paste"})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError || len(result.Content) != 0 {
		t.Fatalf("empty CallTool() result = %#v", result)
	}
	metadata, ok := result.StructuredContent.(map[string]any)
	if !ok || metadata["available"] != false {
		t.Fatalf("empty structured metadata = %#v", result.StructuredContent)
	}
}

func TestMCPReturnsOrderedImageContent(t *testing.T) {
	service := &fakeLatestPasteService{latest: identity.LatestPaste{
		Available: true,
		PasteID:   "00000000-0000-4000-8000-000000000603",
		Images: []identity.ImageAsset{
			{AssetIndex: 0, MIMEType: "image/png", Bytes: []byte{1, 2}},
			{AssetIndex: 1, MIMEType: "image/jpeg", Bytes: []byte{3, 4}},
		},
	}}
	session := connectTestClient(t, service)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_latest_paste", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 2 {
		t.Fatalf("content = %#v", result.Content)
	}
	first, ok := result.Content[0].(*mcp.ImageContent)
	if !ok || first.MIMEType != "image/png" || string(first.Data) != string([]byte{1, 2}) {
		t.Fatalf("first image = %#v", result.Content[0])
	}
	metadata, ok := result.StructuredContent.(map[string]any)
	if !ok || metadata["kind"] != "content" || metadata["assets"] != float64(2) {
		t.Fatalf("image metadata = %#v", result.StructuredContent)
	}
}

func TestMCPAvailableEmptyTextOmitsTextBlock(t *testing.T) {
	session := connectTestClient(t, &fakeLatestPasteService{latest: identity.LatestPaste{
		Available: true, PasteID: "00000000-0000-4000-8000-000000000604", RevisionID: "00000000-0000-4000-8000-000000000605",
	}})
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_latest_paste"})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 0 {
		t.Fatalf("empty text content = %#v", result.Content)
	}
	metadata, ok := result.StructuredContent.(map[string]any)
	if !ok || metadata["available"] != true || metadata["kind"] != "content" || metadata["assets"] != float64(0) {
		t.Fatalf("empty text metadata = %#v", result.StructuredContent)
	}
}

func TestMCPReturnsTextThenOrderedAttachments(t *testing.T) {
	exact := "  line one\r\nline two\ntrailing  "
	createdAt := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	expiresAt := createdAt.Add(24 * time.Hour)
	service := &fakeLatestPasteService{latest: identity.LatestPaste{
		Available: true, PasteID: "00000000-0000-4000-8000-000000000611", RevisionID: "00000000-0000-4000-8000-000000000612",
		ServerSequence: 77, CreatedAt: createdAt, ExpiresAt: expiresAt, Text: exact,
		Images: []identity.ImageAsset{
			{AssetIndex: 0, MIMEType: "image/png", Width: 1, Height: 2, ByteSize: 2, StorageKey: "secret-storage-key", Bytes: []byte{1, 2}},
			{AssetIndex: 1, MIMEType: "image/jpeg", Width: 3, Height: 4, ByteSize: 2, Bytes: []byte{3, 4}},
		},
	}}
	session := connectTestClient(t, service)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_latest_paste", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError || len(result.Content) != 3 {
		t.Fatalf("content = %#v", result.Content)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text != exact {
		t.Fatalf("text = %#v", result.Content[0])
	}
	first, firstOK := result.Content[1].(*mcp.ImageContent)
	second, secondOK := result.Content[2].(*mcp.ImageContent)
	if !firstOK || !secondOK || first.MIMEType != "image/png" || second.MIMEType != "image/jpeg" || !bytes.Equal(first.Data, []byte{1, 2}) || !bytes.Equal(second.Data, []byte{3, 4}) {
		t.Fatalf("images = %#v", result.Content[1:])
	}
	metadata, ok := result.StructuredContent.(map[string]any)
	if !ok || metadata["available"] != true || metadata["kind"] != "content" || metadata["assets"] != float64(2) || metadata["paste_id"] != service.latest.PasteID || metadata["revision_id"] != service.latest.RevisionID || metadata["server_sequence"] != float64(77) {
		t.Fatalf("metadata = %#v", result.StructuredContent)
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		exact, "secret-storage-key", base64.StdEncoding.EncodeToString([]byte{1, 2}), "ciphertext", "nonce",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("structured metadata exposed %q: %s", forbidden, encoded)
		}
	}
}

func connectTestClient(t *testing.T, service *fakeLatestPasteService) *mcp.ClientSession {
	t.Helper()
	handler := NewHandler(service)
	principal := identity.Principal{WorkspaceID: "00000000-0000-4000-8000-000000000600", Scope: "connector"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
	}))
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1.0"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: server.URL, MaxRetries: -1, DisableStandaloneSSE: true}
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}
