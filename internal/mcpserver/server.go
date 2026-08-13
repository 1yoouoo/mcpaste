package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const version = "0.1.0"

type latestPasteService interface {
	LatestPaste(context.Context, identity.Principal) (identity.LatestPaste, error)
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, principal identity.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

func NewHandler(service latestPasteService) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcpaste", Version: version}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "get_latest_paste",
		Description: "Retrieve the latest valid MCPaste text or static-image paste.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false},
	}, getLatestPaste(service))
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

func getLatestPaste(service latestPasteService) mcp.ToolHandler {
	return func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := requireEmptyArguments(request.Params.Arguments); err != nil {
			return nil, err
		}
		principal, ok := ctx.Value(principalKey{}).(identity.Principal)
		if !ok || principal.Scope != "connector" {
			return nil, errors.New("connector context missing")
		}
		latest, err := service.LatestPaste(ctx, principal)
		if err != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "latest paste unavailable"}},
			}, nil
		}
		metadata := map[string]any{
			"available":       latest.Available,
			"kind":            "text",
			"paste_id":        latest.PasteID,
			"revision_id":     latest.RevisionID,
			"server_sequence": latest.ServerSequence,
			"created_at":      latest.CreatedAt,
			"expires_at":      latest.ExpiresAt,
		}
		if len(latest.Images) > 0 {
			metadata["kind"] = "image_bundle"
			metadata["assets"] = len(latest.Images)
		}
		result := &mcp.CallToolResult{StructuredContent: metadata}
		if latest.Available {
			if len(latest.Images) > 0 {
				result.Content = make([]mcp.Content, 0, len(latest.Images))
				for _, image := range latest.Images {
					result.Content = append(result.Content, &mcp.ImageContent{Data: image.Bytes, MIMEType: image.MIMEType})
				}
			} else {
				result.Content = []mcp.Content{&mcp.TextContent{Text: latest.Text}}
			}
		}
		return result, nil
	}
}

func requireEmptyArguments(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil || len(fields) != 0 {
		return errors.New("get_latest_paste accepts no arguments")
	}
	return nil
}
