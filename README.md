# MCPaste

MCPaste is a local-first macOS app that makes one temporary text-and-image context available to MCP-compatible AI clients across Macs in the same Tailscale tailnet.

## Requirements

- macOS 14 or later on every participating Mac
- Tailscale installed and running on every Mac
- All N Macs signed in to the same tailnet

MCPaste works on one Mac too. To synchronize N Macs, install and open the same app on all N; no Mac has a special role.

## Install MCPaste 0.2.1

MCPaste requires macOS 14 or later and [Tailscale](https://tailscale.com/download). Install the latest signed release with one command:

```sh
curl -fsSL https://raw.githubusercontent.com/1yoouoo/mcpaste/main/install.sh | sh
```

The installer downloads [MCPaste v0.2.1](https://github.com/1yoouoo/mcpaste/releases/tag/v0.2.1) from GitHub Releases, verifies its SHA-256 checksum, installs it at `/Applications/MCPaste.app`, and links its embedded helper at `/usr/local/bin/mcpaste`. It prefers the notarized `MCPaste-final.zip` and falls back to the ad-hoc `MCPaste-adhoc.zip` when Apple signing credentials are unavailable.

To share the installation with a person or AI assistant, send this repository URL together with “download and install MCPaste”: <https://github.com/1yoouoo/mcpaste>

For a manual install, download the macOS ZIP from the [v0.2.1 release](https://github.com/1yoouoo/mcpaste/releases/tag/v0.2.1), unzip `MCPaste.app` into `/Applications`, and open it. The one-line installer is recommended because it also installs the embedded MCP helper and verifies the release checksum.

Open MCPaste on each Mac. Restart the desired MCP client after registration so it loads the new MCP entry.

## How it works

MCPaste exposes exactly one global current context: exact text plus ordered images. Editing on any Mac atomically replaces the whole context everywhere; text and images never merge independently.

```text
 MCP-compatible client
          |
        STDIO
          |
 embedded mcpaste helper
          |
 authenticated loopback
          |
 MCPaste runtime on Mac A  <==== Tailscale tailnet ====>  MCPaste runtime on Mac B ... Mac N
          |                                                   |
   in-memory context                                  in-memory replica
```

Each running app discovers other MCPaste Macs through the local Tailscale CLI. Complete snapshots converge with deterministic last-write-wins ordering. A later edit on any Mac becomes the new complete context.

The app shows exactly four synchronization states:

- Up to date
- Updating…
- Waiting to sync
- Source offline

When the current source is offline, an in-memory replica remains visible in the GUI so you can recover it by editing. The MCP tool refuses that replica and returns unavailable until the source returns or a local edit creates a new current context.

## Connect an MCP-compatible client

The embedded helper is a client-neutral STDIO MCP process exposing one read-only tool, `get_latest_paste`, which retrieves the current MCPaste context as exact text followed by ordered image content blocks.

For any MCP-compatible client, add an STDIO entry with these values:

```text
name: mcpaste
transport: stdio
command: /Applications/MCPaste.app/Contents/Helpers/mcpaste
arguments: []
```

Do not add a bearer value or network address to the client entry. The helper reads its owner-only local credential file and connects to the running app over loopback.

MCPaste automatically registers the embedded command only for a Codex config at `~/.codex/config.toml` or `CODEX_CONFIG_PATH`, and a Claude Code config at `~/.claude.json`, `CLAUDE_CONFIG_PATH`, or `$CLAUDE_CONFIG_DIR/.claude.json`. Other MCP-compatible clients use the generic STDIO values above. If a supported config is created after MCPaste starts, reopen MCPaste and then restart that client.

MCPaste also exposes the prompt `MCPaste: Use current context`. In an MCP client that provides a Prompt UI, select it before a task; it instructs the model to call `get_latest_paste` and use the returned text and ordered images. This does not install a skill or edit global instruction files. Clients that do not expose MCP Prompts can continue using the `get_latest_paste` tool directly.

## Context lifetime

The context lifetime is memory-only and ephemeral. A running peer may hold the current replica so another Mac can reconnect, but content is not written to files, preferences, client configuration, or logs.

When all MCPaste replicas and their runtimes exit, the context is lost. Opening the app later starts empty unless another still-running peer still holds the context.

## Security and privacy

Tailnet membership and Tailscale identity are the peer trust boundary. A random per-install bearer token protects the local loopback routes used by the app and embedded helper. See [Security Policy](SECURITY.md) and [Security and secrets](docs/security-and-secrets.md).

An AI client or model provider may retain content after MCPaste returns it. That downstream retention is outside MCPaste's scope.

## Development

Use the Go version in `.go-version` and full Xcode 15.4 or later. The supported checks are:

```sh
go mod tidy -diff
go mod verify
test -z "$(gofmt -l cmd/mcpaste internal/connector internal/peer)"
go vet ./cmd/mcpaste ./internal/connector ./internal/peer
go test -race ./cmd/mcpaste ./internal/connector ./internal/peer
(cd macos/MCPaste && swift test && swift build -c release)
```

Release artifact verification is documented in [Releases](docs/releases.md).
