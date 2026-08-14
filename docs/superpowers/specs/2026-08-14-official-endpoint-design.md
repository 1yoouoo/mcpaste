# Official MCPaste Endpoint Design

**Status:** Approved

## Goal

Make MCPaste use the hosted service at `https://mcpaste.1yoouoo.com` by default and by design, without asking users to enter an endpoint in the macOS app or CLI.

## Decision

MCPaste will be an official-server-only client. The public macOS onboarding flow and the Go setup CLI will not accept a user-supplied endpoint. Both clients will use the canonical hosted endpoint internally.

Self-hosted, local, and alternate-server workflows are out of scope for the supported clients after this change. The server API itself is unchanged.

## Scope

### macOS client

- Remove the endpoint text field from `OnboardingView`.
- Remove endpoint input validation and endpoint state from the onboarding model.
- Route create, join, recover, and authenticated sessions through the canonical hosted API URL.
- Continue storing the canonical endpoint with the existing Keychain credential schema for persistence compatibility.
- During restore, reject a stored credential whose endpoint is absent or is not the canonical endpoint before sending its token anywhere. Do not delete that credential; show a generic re-pairing error instead.

### Go CLI and MCP connector

- Remove the public `--endpoint` setup and proxy options.
- Make `mcpaste setup --name ...` use the canonical API and MCP URLs internally.
- Generated Codex and Claude Code configuration must no longer include an endpoint argument.
- Continue reading the existing credential file format for compatibility, but reject non-canonical stored endpoints before making a network request.
- Save only the canonical endpoint in newly written connector credentials.

### Documentation

- Update current README setup instructions to omit endpoint arguments and describe the official hosted service.
- Remove current user-facing self-hosting/alternate-endpoint instructions.
- Do not rewrite historical planning or delivery records unless they contain current user instructions that would otherwise be misleading.

## Endpoint constants

Each language client will own one private/internal canonical endpoint constant because the Swift package and Go module do not share source files:

- API base: `https://mcpaste.1yoouoo.com`
- MCP URL: `https://mcpaste.1yoouoo.com/v1/mcp`

Tests may inject loopback or test-server URLs through internal constructors or test-only seams. No user-facing flag or environment override will be added.

## Compatibility and security

- Existing Keychain items and connector credential files are not deleted or rewritten merely because they contain an old endpoint.
- A non-canonical stored endpoint is rejected before its bearer token is sent to the official service.
- Existing official credentials continue to restore normally.
- Existing self-hosted credentials require a new official-server pairing or recovery flow; the client does not expose a replacement endpoint field.
- Tokens, recovery codes, pairing secrets, and credential contents remain excluded from logs, screenshots, documentation examples, and test output.

## Verification

The implementation must add or update tests before production code changes:

- Swift tests verify the canonical endpoint and that onboarding no longer requires endpoint state/input.
- Go tests verify setup uses the canonical endpoint, rejects the removed flag, omits endpoint arguments from generated client configuration, and rejects non-canonical stored credentials without network transmission.
- Full verification runs `swift test`, `swift build -c release`, and the relevant Go test suite.
- The local macOS UI test confirms the onboarding screen no longer displays an endpoint field and still exposes create, join, and recovery flows.

## Out of scope

- Apple Developer signing, notarization, GitHub tags, GitHub Releases, deployment, or push.
- Server API changes.
- General UI redesign or visual polish.
- Deleting or resetting existing local user data.
