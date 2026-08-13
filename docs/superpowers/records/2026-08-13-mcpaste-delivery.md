# MCPaste Phase 6 delivery handoff

Date: 2026-08-13

## Implemented

- Added production Compose topology for Caddy, the non-root Go server, PostgreSQL, and a root-only data-volume initializer. PostgreSQL has no public port; database and image data use persistent volumes.
- Added protected host bootstrap, digest-only deployment, forward migration, readiness and authenticated MCP smoke checks, application-only rollback, and full deployment-environment preservation.
- Added pinned GitHub Actions for CI, GHCR deployment, Linux `amd64`/`arm64` artifacts, and macOS SwiftPM app packaging, Developer ID signing, notarization, stapling, and checksums. No signing or release command ran locally.
- Added operator documentation for first boot, DNS/TLS, secret permissions, retention, rollback, release verification, key handling, and the accepted no-backup risk.
- Closed review gaps across the Phase 4/5 client and server paths: recovery onboarding, cached sync snapshot replacement, image bundle limits, image/text paste type isolation, image cleanup on failed publication, authenticated deployment smoke enforcement, macOS clipboard image input, and SwiftPM app packaging.

## Verification

- `go test ./... -count=1`: passed with the local Compose PostgreSQL service.
- `go test -race ./... -count=1`: passed with the local Compose PostgreSQL service.
- `go vet ./...`, `go mod tidy -diff`, and `go mod verify`: passed in Go 1.26.5 Docker.
- Linux connector build for `amd64` and `arm64`: passed with `CGO_ENABLED=0`.
- PostgreSQL migration, identity, HTTP, image, MCP, and connector integration tests: passed.
- Real STDIO process E2E: passed; a child `mcpaste` process retrieved exact remote text and the structured empty result through the official MCP SDK.
- Deployment simulation: failed application start restored the complete previous environment, and rollback changed only the application image without down migrations.
- Swift core and app SDK typechecks against the installed macOS 14 SDK: passed.
- Shell syntax, Compose contract, workflow contract, YAML parsing, `git diff --check`, and Gitleaks `v8.24.3`: passed; no leaks found across 62 commits.

## Environment limits

- Full `swift test` and `xcodebuild` could not run on this host because only Command Line Tools are selected and `xcodebuild` is unavailable. `swift test` fails at the missing SDK `PlatformPath` lookup. CI and the release workflow require full Xcode.
- The production Docker build could not be completed locally because Docker stalled while resolving the pinned `golang:1.26.5-alpine` base image and was cancelled. Go compilation and all code checks passed in the pinned Go 1.26.5 tool container; CI must perform the actual image build before deployment.
- No GitHub push, tag, release, GHCR publication, DigitalOcean deployment, Apple signing, notarization, or real credential creation was performed.

## Owner checkpoints

1. Install and select the full Xcode version required by the workflows, then run `swift test`, `swift build -c release`, and the macOS packaging/signing checks.
2. Provision the production host, DNS/TLS name, `/etc/mcpaste/server.env` with real secrets, and `/etc/mcpaste/deploy.env` with an immutable image reference. Keep both files root-owned and mode `0600`.
3. Configure the protected GitHub `production` and `release` environments, including known hosts, SSH deploy key, health endpoint, MCP smoke token, Apple signing certificate, Team ID, Developer ID identity, and notarization credentials.
4. Review the clean local diff and commits, then explicitly authorize the first push/tag/release/deployment. Verify Linux checksums, macOS `codesign`/`spctl`, TLS, authenticated MCP retrieval, connector read-only behavior, and rollback in the real environment.
