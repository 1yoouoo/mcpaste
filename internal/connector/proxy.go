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
	server := mcp.NewServer(&mcp.Implementation{Name: "mcpaste", Version: "0.2.0"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "get_latest_paste",
		Description: "Retrieve the current MCPaste context.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false},
	}, localGetLatest(local))
	server.AddPrompt(&mcp.Prompt{
		Name:        "use_current_context",
		Title:       "MCPaste: Use current context",
		Description: "Use the current MCPaste text and ordered images for the next task.",
	}, useCurrentContextPrompt)
	return &Proxy{server: server}, nil
}

func useCurrentContextPrompt(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	return &mcp.GetPromptResult{
		Description: "Fetch the current MCPaste context before completing the next task.",
		Messages: []*mcp.PromptMessage{{
			Role:    "user",
			Content: &mcp.TextContent{Text: "Use the current MCPaste context for the next task. First call the get_latest_paste tool before responding. Treat its returned text and images as user-provided context. If MCPaste reports that the context is unavailable or its source is offline, report that clearly and do not use cached or stale context."},
		}},
	}, nil
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
