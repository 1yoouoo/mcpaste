package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/1yoouoo/mcpaste/internal/connector"
	"github.com/1yoouoo/mcpaste/internal/peer"
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
		switch args[0] {
		case "peer":
			return runPeer(ctx, args[1:], os.Stdin, os.Stdout)
		case "register":
			return runRegister(args[1:], os.Stdout)
		default:
			if args[0] == "" || args[0][0] != '-' {
				return errors.New("invalid command")
			}
		}
	}
	return runMCP(ctx, args)
}

func runMCP(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("mcpaste", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	credentialPath := flags.String("credential-file", "", "local credential file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("invalid MCP arguments")
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
	proxy, err := connector.NewProxy(credential)
	if err != nil {
		return err
	}
	return proxy.Server().Run(ctx, &mcp.StdioTransport{})
}

func runPeer(ctx context.Context, args []string, stdin *os.File, readiness io.Writer) error {
	flags := flag.NewFlagSet("mcpaste peer", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	deviceID := flags.String("device-id", "", "device ID")
	name := flags.String("name", "", "device display name")
	credentialPath := flags.String("credential-file", "", "local credential file")
	registryPath := flags.String("registry-file", "", "peer registry file")
	port := flags.Int("port", peer.DefaultPort, "peer runtime port")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *deviceID == "" || *name == "" {
		return errors.New("invalid peer arguments")
	}
	if *credentialPath == "" {
		var err error
		*credentialPath, err = connector.DefaultCredentialPath()
		if err != nil {
			return err
		}
	}
	if *registryPath == "" {
		*registryPath = filepath.Join(filepath.Dir(*credentialPath), "peers.json")
	}
	return peer.Run(ctx, peer.RuntimeOptions{
		DeviceID:       *deviceID,
		DisplayName:    *name,
		Port:           *port,
		CredentialPath: *credentialPath,
		RegistryPath:   *registryPath,
		Stdin:          stdin,
		Readiness:      readiness,
		Tailscale:      peer.TailscaleRunner{},
		Now:            time.Now,
	})
}

func runRegister(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("mcpaste register", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	codexPath := flags.String("codex-config", "", "Codex configuration path")
	claudePath := flags.String("claude-config", "", "Claude Code configuration path")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || stdout == nil {
		return errors.New("invalid register arguments")
	}
	commandPath, err := os.Executable()
	if err != nil {
		return errors.New("resolve mcpaste executable")
	}
	configured, err := connector.ConfigureClients(connector.ClientConfigOptions{
		CommandPath: commandPath,
		CodexPath:   *codexPath,
		ClaudePath:  *claudePath,
	})
	if err != nil {
		return err
	}
	if err := json.NewEncoder(stdout).Encode(configured); err != nil {
		return errors.New("write registration result")
	}
	return nil
}
