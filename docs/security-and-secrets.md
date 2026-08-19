# Security and secrets management

This document covers the current local macOS product and its tailnet peer traffic.

## Trust boundaries

MCPaste uses two independent trust boundaries:

1. Same-tailnet membership and Tailscale identity authorize peer traffic. The runtime refreshes the local Tailscale status snapshot and accepts peer routes only from source addresses present in that snapshot.
2. A random per-install bearer token authorizes loopback traffic between the macOS app and the embedded STDIO helper. The loopback listener alone is not an authorization boundary.

Peer display names and Tailscale status fields are untrusted input. They must never be interpolated into a shell command. Tailscale is invoked as an executable with a fixed argument array.

## Local data placement

| Data | Current placement | Prohibited placement |
| --- | --- | --- |
| Current text and normalized images | Memory in running MCPaste runtimes | Files, preferences, credential files, client configuration, logs, crash annotations, fixtures |
| Local loopback bearer token | `~/.config/mcpaste/credential.json`, or `$XDG_CONFIG_HOME/mcpaste/credential.json`, mode `0600` | Model client config, process arguments, logs, screenshots, repository |
| Device identifier and display name | Local non-content preferences | Logs that associate them with context content |
| Known peer names and last-seen times | Local content-free peer registry | Context payloads or authentication decisions beyond current Tailscale source validation |

The credential directory is owner-only. Credential reads reject links, non-regular files, oversized data, unknown fields, and group/other permissions. Credential replacement uses a descriptor-anchored temporary file, mode `0600`, atomic rename, and directory sync.

Client configuration contains only the absolute embedded-helper command and an empty argument list. Never copy the local token or loopback address into a model client configuration or command line.

## Memory-only context lifetime

Each running runtime keeps at most the current complete text-and-image snapshot plus short-lived staged assets. Context bodies are not persisted. If every process holding a replica exits, the context is gone.

The GUI may display an existing in-memory replica while its source is offline. The MCP tool must refuse that replica and return unavailable. A deliberate local edit creates a new local revision and makes that Mac the source.

## Peer traffic

Tailscale encrypts traffic between Macs. MCPaste peer routes additionally verify that the request source is present in the current local Tailscale snapshot. Loopback routes require the local bearer token even when called from the same Mac.

Request and response decoders enforce bounded JSON, exact text, image count, per-image size, and complete-bundle limits before accepting a snapshot. Image bytes are accepted only after declared length and SHA-256 digest verification. Partial snapshots never replace the current complete context.

## Logging and capture safety

Logs may contain bounded operational metadata such as time, route, status, duration, size, and count. Logs must exclude:

- context text and image bytes;
- authorization headers and local bearer values;
- complete request or response bodies;
- complete Tailscale status output;
- model prompts or returned MCP content.

Use fake deterministic payloads in tests and documentation. Do not attach real context, credentials, screenshots containing content, or machine-identifying Tailscale output to issues or pull requests.

## Local checks

Before sharing changes, inspect the diff and run the repository secret-pattern audit:

```sh
git diff --check
git grep -n -E 'sk-[A-Za-z0-9]{12,}|ghp_[A-Za-z0-9]{12,}|AKIA[0-9A-Z]{16}|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY' -- . ':!.github/workflows/ci.yml' ':!docs/security-and-secrets.md' ':!docs/superpowers/**'
```

The expected grep result is exit 1 with no output. This pattern check supplements review and does not prove that a repository is free of sensitive data.

## Downstream retention and incidents

Once an MCP-compatible client receives context, its local logs and the selected model provider's retention behavior are outside MCPaste's scope. Review those products' data controls separately.

If a local token is exposed, quit MCPaste, remove the credential file, and reopen the app to create a new token. If tailnet access is no longer trusted, remove the affected device in Tailscale before reopening MCPaste. Do not paste an exposed value or sensitive context into an incident report.
