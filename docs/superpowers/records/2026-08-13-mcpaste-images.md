# MCPaste static image phase handoff

Date: 2026-08-13

## Implemented

- Added migration `000003_image_bundles` with image revisions, ordered asset metadata, composite workspace/revision ownership, and 24-hour expiry checks.
- Added AES-GCM encrypted file storage below `MCPASTE_DATA_DIR`; writes use private directories, exclusive temporary files, fsync, and atomic rename.
- Added bounded multipart image upload, normalized metadata validation, full-scope authorization, range rejection, explicit deletion cleanup, and expiry cleanup.
- Added image bundle metadata to history and incremental sync without returning image bytes.
- Added ordered MCP `ImageContent` blocks while preserving the existing exact-text behavior and empty-result behavior.
- Added macOS ImageIO normalization to metadata-free PNG output, drag/drop upload wiring, and multipart API upload support.

## Verification

- Go package tests passed in Docker with Go 1.26.5.
- PostgreSQL migrations applied successfully through version 3 using the repository Compose PostgreSQL service.
- PostgreSQL identity/image integration test passed for encrypted image persistence, 24-hour expiry metadata, connector read behavior, and connector write rejection.
- HTTP image upload test passed for normalized multipart metadata and connector 403 rejection.
- MCP image ordering test passed with the official Go SDK.
- Swift core and app sources passed `swiftc -typecheck` against the installed macOS 14 SDK.
- SwiftPM tests remain blocked by the active Command Line Tools SDK metadata issue; full Xcode validation is deferred to the owner action in the delivery runbook.

## Known operational boundary

- Image bytes are never included in sync/SSE metadata. Full-scope authenticated image download and connector MCP retrieval are separate read paths.
- No production data directory, encryption key, image bytes, or credentials were created or tracked.
