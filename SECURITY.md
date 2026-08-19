# Security Policy

MCPaste handles arbitrary text and images. Treat them as sensitive by default.

## Trust boundaries

Peer trust is defined by same-tailnet membership and Tailscale identity. Peer routes accept requests only when the source address appears in the local Tailscale status snapshot. Tailscale protects peer traffic in transit; MCPaste does not add a second transport-encryption layer inside the tailnet.

Local trust is separate. Each installation creates a random local loopback bearer token and stores it in an owner-only credential file. The local token never appears in model client configuration, process arguments, or logs. Knowing the loopback port is not sufficient to read or change context.

## Content handling

- Context text and normalized image bytes remain memory-only.
- Content is not written to files, preferences, credential files, client configuration, or logs.
- Complete peer responses and complete Tailscale status output are not logged.
- When every process holding a replica exits, the context is lost.
- If the current source is offline, the GUI may show its in-memory replica, but the MCP tool refuses it and returns unavailable.
- Retention by downstream AI clients, models, or providers is outside MCPaste's scope.

Persistent local metadata is limited to the device identifier, display name, known peer names and last-seen times, and the local loopback credential.

## Contributor rules

- Never commit real credentials, private keys, personal data, context text, or image content.
- Use visibly fake deterministic examples in fixtures and documentation.
- Never log request or response bodies, authorization headers, bearer values, image bytes, or complete Tailscale output.
- Keep Apple signing and notarization credentials in the protected release environment, never in the repository or app bundle.
- Preserve request, manifest, text, asset-count, per-asset, and complete-bundle limits on every decode path.

## Incident response

1. Rotate the exposed local credential or remove the affected Mac from the tailnet.
2. Do not copy the sensitive value or content into an issue, pull request, log, or report.
3. Record only the affected file or version, time, impact, and remediation status.
4. Report the incident privately.

## Reporting a vulnerability

Use GitHub private vulnerability reporting when available. Otherwise contact the maintainer through the contact details on their GitHub profile. Include reproduction steps, impact, affected version or commit, and mitigation details, using fake payloads instead of live sensitive content.
