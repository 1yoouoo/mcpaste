# MCPaste

MCPaste is a macOS menu bar app that deliberately hands plain text and static images to AI coding tools through MCP.

> Status: early development. The approved design exists, but the app, service, and connector are unreleased.

## Product boundary

- MCPaste does not automatically monitor the clipboard.
- The full macOS app is the supported create/edit/delete interface.
- Codex, Claude Code, and headless Linux companions are read-only.
- The MCPaste service is the central source of truth.
- Treat data as sensitive by default.

## Trust model

MCPaste is not end-to-end encrypted: the service decrypts data. TLS protects data in transit and application encryption protects data at rest, but an operator or a full server compromise can read it. Retention by downstream AI tools and providers is outside MCPaste's control.

See [Security and secrets](docs/security-and-secrets.md) and the [Security Policy](SECURITY.md).

## Planned text architecture

```text
Full Mac app --HTTPS write/sync--> MCPaste service --> PostgreSQL/files
Codex / Claude Code --STDIO--> mcpaste connector --Streamable HTTP MCP--> MCPaste service
```

## Production MVP

The production MVP is one DigitalOcean Droplet with Caddy, the Go service, and PostgreSQL. The implementation consists of a SwiftUI app, Go service, and Go connector.

## Development prerequisites

- Go version from `.go-version`
- Docker
- Full Xcode only during the macOS app phase

Run the Go checks and server with:

```sh
go test ./...
go run ./cmd/server
```

Never use real secrets in environment files, fixtures, logs, screenshots, issues, or pull requests. Use visibly fake deterministic examples such as `example-token-not-real`.

Read the [approved system design](docs/superpowers/specs/2026-08-12-mcpaste-system-design.md) and [implementation roadmap](docs/superpowers/plans/2026-08-12-mcpaste-roadmap.md) for the current direction.
