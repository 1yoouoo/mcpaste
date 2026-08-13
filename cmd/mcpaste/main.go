package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/1yoouoo/mcpaste/internal/connector"
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
		return errors.New("setup is not available yet")
	}
	path, err := connector.DefaultCredentialPath()
	if err != nil {
		return err
	}
	credential, err := connector.LoadCredential(path)
	if err != nil {
		return err
	}
	proxy, err := connector.NewProxy(ctx, credential, http.DefaultClient)
	if err != nil {
		return err
	}
	defer proxy.Close()
	return proxy.Server().Run(ctx, &mcp.StdioTransport{})
}
