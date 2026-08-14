# Official MCPaste Endpoint Design

**Status:** Approved; amended for build-time configuration and repository cleanup

## Goal

Make MCPaste use the hosted service at `https://mcpaste.1yoouoo.com` by default and by design, without asking users to enter an endpoint in the macOS app or CLI.

## Decision

MCPaste will be an official-server-only client. The public macOS onboarding flow and the Go setup CLI will not accept a user-supplied endpoint. Both clients will use a build-time `MCPASTE_ENDPOINT` value supplied by the release/build environment.

Self-hosted, local, and alternate-server workflows are out of scope for the supported clients after this change. The server API itself is unchanged.

## Scope

### macOS client

- Remove the endpoint text field from `OnboardingView`.
- Remove endpoint input validation and endpoint state from the onboarding model.
- Route create, join, recover, and authenticated sessions through the build-injected hosted API URL.
- Continue storing the canonical endpoint with the existing Keychain credential schema for persistence compatibility.
- During restore, reject a stored credential whose endpoint is absent or is not the canonical endpoint before sending its token anywhere. Do not delete that credential; show a generic re-pairing error instead.

### Go CLI and MCP connector

- Remove the public `--endpoint` setup and proxy options.
- Make `mcpaste setup --name ...` use the build-injected API and MCP URLs internally.
- Generated Codex and Claude Code configuration must no longer include an endpoint argument.
- Continue reading the existing credential file format for compatibility, but reject non-canonical stored endpoints before making a network request.
- Save only the canonical endpoint in newly written connector credentials.

### Documentation

- Update current README setup instructions to omit endpoint arguments and describe the official hosted service.
- Remove current user-facing self-hosting/alternate-endpoint instructions.
- Do not rewrite historical planning or delivery records unless they contain current user instructions that would otherwise be misleading.

## Endpoint constants

Each language client will own one private/internal endpoint configuration because the Swift package and Go module do not share source files. The value is generated or linked from the build environment rather than written as a URL literal in source:

- Build variable: `MCPASTE_ENDPOINT`
- API base: `${MCPASTE_ENDPOINT}`
- MCP URL: `${MCPASTE_ENDPOINT}/v1/mcp`

The build must reject an unset, malformed, non-HTTPS, or path-bearing endpoint before producing a distributable client. Tests may inject loopback or test-server URLs through internal constructors or test-only seams. No user-facing flag or runtime endpoint override will be added. The environment variable is configuration, not a secret or a security boundary.

## Compatibility and security

- Existing Keychain items and connector credential files are not deleted or rewritten merely because they contain an old endpoint.
- A non-canonical stored endpoint is rejected before its bearer token is sent to the official service.
- Existing credentials whose stored endpoint matches the build-injected endpoint continue to restore normally.
- Existing self-hosted credentials require a new official-server pairing or recovery flow; the client does not expose a replacement endpoint field.
- Tokens, recovery codes, pairing secrets, and credential contents remain excluded from logs, screenshots, documentation examples, and test output.

## Repository cleanup

- Keep `docs/operations.md`, `docs/releases.md`, and `docs/security-and-secrets.md` as public project documentation.
- Remove `docs/superpowers/` from Git tracking without deleting the local files, then ignore it for future local planning records.
- Ignore local AI working directories such as `/.superpowers/`, `/.codex/`, and `/.claude/` when present; do not ignore project source, tests, or product assets.
- Preserve the untracked `assets/` product artwork; it is not an AI working directory.

## Verification

The implementation must add or update tests before production code changes:

- Swift tests verify build-injected endpoint configuration and that onboarding no longer requires endpoint state/input.
- Go tests verify build-injected endpoint configuration, rejects the removed flag, omits endpoint arguments from generated client configuration, and rejects non-canonical stored credentials without network transmission.
- Configuration checks verify missing, malformed, non-HTTPS, and path-bearing `MCPASTE_ENDPOINT` values fail before a release build.
- Full verification runs `MCPASTE_ENDPOINT=... swift test`, `MCPASTE_ENDPOINT=... swift build -c release`, and the relevant Go test suite with the same build variable.
- The local macOS UI test confirms the onboarding screen no longer displays an endpoint field and still exposes create, join, and recovery flows.

## Out of scope

- Apple Developer signing, notarization, GitHub tags, GitHub Releases, deployment, or push.
- Server API changes.
- General UI redesign or visual polish.
- Deleting or resetting existing local user data.
