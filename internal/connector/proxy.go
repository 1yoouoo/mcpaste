package connector

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Proxy struct {
	server *mcp.Server
}

func NewProxy(credential Credential) (*Proxy, error) {
	local, err := NewLocalClient(credential, nil)
	if err != nil {
		return nil, err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "mcpaste", Version: "0.1.0"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "get_latest_paste",
		Description: "Retrieve the current MCPaste context.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false},
	}, localGetLatest(local))
	return &Proxy{server: server}, nil
}

func localGetLatest(client *LocalClient) mcp.ToolHandler {
	return func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		localContext, err := client.Read(ctx)
		if err != nil {
			message := "MCPaste context error."
			if errors.Is(err, ErrLocalUnavailable) || errors.Is(err, ErrNoContext) || errors.Is(err, ErrSourceOffline) {
				message = "MCPaste context unavailable."
			}
			return &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: message}},
				StructuredContent: map[string]any{"available": false},
				IsError:           true,
			}, nil
		}
		content := make([]mcp.Content, 1, len(localContext.Assets)+1)
		content[0] = &mcp.TextContent{Text: localContext.Manifest.Text}
		for index, data := range localContext.Assets {
			content = append(content, &mcp.ImageContent{Data: data, MIMEType: localContext.Manifest.Assets[index].MIMEType})
		}
		return &mcp.CallToolResult{
			Content: content,
			StructuredContent: map[string]any{
				"available":        true,
				"revision":         localContext.Manifest.Revision,
				"source_device_id": localContext.Manifest.SourceDeviceID,
				"assets":           localContext.Manifest.Assets,
			},
		}, nil
	}
}

func (p *Proxy) Server() *mcp.Server {
	return p.server
}
