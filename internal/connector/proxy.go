package connector

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Proxy struct {
	server  *mcp.Server
	session *mcp.ClientSession
}

func NewProxy(ctx context.Context, credential Credential, client *http.Client) (*Proxy, error) {
	if credential.Endpoint == "" || credential.Token == "" {
		return nil, errors.New("invalid connector credential")
	}
	if err := ValidateEndpoint(credential.Endpoint); err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	clientCopy := *client
	base := clientCopy.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clientCopy.Transport = bearerTransport{base: base, token: credential.Token}
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("MCP endpoint redirects are not allowed")
	}
	remoteClient := mcp.NewClient(&mcp.Implementation{Name: "mcpaste-connector", Version: "0.1.0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint: credential.Endpoint, HTTPClient: &clientCopy, MaxRetries: -1, DisableStandaloneSSE: true,
	}
	session, err := remoteClient.Connect(ctx, transport, nil)
	if err != nil {
		return nil, errors.New("connect to remote MCP")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "mcpaste", Version: "0.1.0"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "get_latest_paste",
		Description: "Retrieve the latest valid MCPaste text paste.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false},
	}, func(callContext context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var arguments any
		if len(request.Params.Arguments) != 0 {
			if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
				return nil, errors.New("invalid MCP arguments")
			}
		}
		return session.CallTool(callContext, &mcp.CallToolParams{Name: "get_latest_paste", Arguments: arguments})
	})
	return &Proxy{server: server, session: session}, nil
}

func (p *Proxy) Server() *mcp.Server {
	return p.server
}

func (p *Proxy) Handler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return p.server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

func (p *Proxy) Close() error {
	if p == nil || p.session == nil {
		return nil
	}
	return p.session.Close()
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}
