# MCPaste MVP implementation roadmap

**Source design:** `docs/superpowers/specs/2026-08-12-mcpaste-system-design.md`

## Why the work is split

The approved design spans a native macOS app, a central service, two MCP transports, a headless Linux companion, image processing, authentication, synchronization, and production delivery. Treating that as one execution plan would couple independent systems and make later steps depend on unverified assumptions.

Implementation is therefore divided into six sequential plans. Each plan must leave the repository in a working, testable state and must be written or refreshed against the code produced by the previous plan.

## Phase 1: Repository foundation and server skeleton

**Detailed plan:** `docs/superpowers/plans/2026-08-12-mcpaste-foundation.md`

Deliverables:

- reconcile public documentation and configuration with the MCPaste design;
- establish the Go module and version baseline;
- add validated server configuration;
- provide metadata-only request logging and `/livez` and `/readyz` endpoints;
- package the server as a minimal container;
- add pull-request and `main` CI checks without deployment credentials.

Acceptance signal: `go test -race ./...`, `go vet ./...`, the server container health smoke test, and the secret scan all pass.

## Phase 2: Workspace identity, pairing, and encrypted persistence

**Detailed plan:** `docs/superpowers/plans/2026-08-12-mcpaste-identity-server.md`

Deliverables:

- PostgreSQL schema and migrations for workspaces, devices, credentials, pairing requests, recovery verifiers, events, and idempotency;
- AES-256-GCM envelope format and server key loading;
- anonymous workspace creation;
- five-minute QR/short-code pairing requests and full-device approval;
- separate full-app and read-only connector credentials;
- device naming, renaming, listing, and revocation;
- Argon2id recovery that adds a full Mac, rotates the code, and preserves existing devices;
- authorization, rate-limit, isolation, and redaction tests.

Acceptance signal: API integration tests create a workspace, pair full and read-only devices, recover another full device, and prove cross-workspace and read-only write attempts are rejected.

## Phase 3: Text timeline, synchronization, and MCP connector

**Planned document:** `docs/superpowers/plans/2026-08-12-mcpaste-text-mcp.md`

Deliverables:

- text paste, immutable revision, tombstone, and one-year purge data paths;
- server-sequence last-write-wins and idempotent mutations;
- durable event cursors, incremental sync, Server-Sent Events, and polling fallback contract;
- `get_latest_paste` text and empty results through the official Go MCP SDK;
- Streamable HTTP MCP authentication;
- `mcpaste` STDIO proxy and Linux credential storage;
- Codex and Claude Code configuration detection and idempotent setup;
- Linux `amd64` and `arm64` builds.

Acceptance signal: a real STDIO MCP client process retrieves exact text from the remote MCP endpoint while every connector write attempt remains unavailable.

## Phase 4: Native macOS app for onboarding and text

**Planned document:** `docs/superpowers/plans/2026-08-12-mcpaste-macos-text.md`

Deliverables:

- SwiftUI menu bar app and approved popover layout;
- new-user, existing-user, pairing approval, and recovery funnels;
- Keychain credentials and separate read-only connector provisioning;
- SQLite cache, thirty-day history, durable cursor, and offline text queue;
- exact-text paste, create, edit, delete, sync status, and last MCP access time;
- device list and rename/revoke controls;
- safe Codex and Claude Code connector configuration;
- macOS 14 minimum deployment target and Intel/Apple Silicon builds.

Acceptance signal: two app instances against the integration service synchronize text and device state, and app UI tests cover new/existing/recovery flows.

## Phase 5: Static-image flow

**Planned document:** `docs/superpowers/plans/2026-08-12-mcpaste-images.md`

Deliverables:

- ImageIO validation, bounded decoding, metadata stripping, JPEG/PNG normalization, and bundle limits;
- drag-and-drop, clipboard paste, multi-image preview, and per-item failures;
- atomic upload and publish protocol;
- encrypted file storage and authenticated reads;
- MCP image content blocks in bundle order;
- immediate explicit deletion, hard 24-hour read expiry, and one-minute cleanup;
- expired-image fallback to the next valid paste.

Acceptance signal: supported fixtures reach a real MCP client as image blocks, unsupported/animated fixtures fail safely, and expiry physically removes server files.

## Phase 6: Production delivery and release

**Planned document:** `docs/superpowers/plans/2026-08-12-mcpaste-delivery.md`

Deliverables:

- production Docker Compose for Caddy, Go service, and PostgreSQL;
- Droplet bootstrap and `/etc/mcpaste/server.env` permissions;
- `main` image build, GHCR push, SSH deployment, migrations, health smoke test, and application rollback;
- tag-triggered signed/notarized macOS and checksummed Linux GitHub Releases;
- current Codex and Claude Code end-to-end smoke tests;
- operator runbook for key handling, failure recovery, retention jobs, and the accepted no-backup risk.

Acceptance signal: a test deployment exercises both successful rollout and unhealthy-container rollback; a release candidate installs on clean macOS and Linux environments and completes the approved end-to-end flows.

## External owner checkpoints

These actions require owner authority and are not silently performed by an implementation agent:

1. Rename the GitHub repository from `paste-bridge` to `mcpaste` before the first push, then approve adding the new `origin` URL.
2. Choose an open-source license before the repository is announced publicly.
3. Install Go 1.26.5 before executing Phase 1.
4. Install and select full Xcode before executing Phase 4.
5. Create the DigitalOcean Droplet, select a domain, and provide DNS only when Phase 6 reaches its production bootstrap checkpoint.
6. Provide Apple Developer signing and notarization access only through protected local/GitHub mechanisms during Phase 6.

No Droplet or paid service is needed for Phases 1 through 3; PostgreSQL runs locally in a container for integration tests.

## Design coverage check

| Approved design area | Implemented by |
| --- | --- |
| Purpose, product boundary, positioning, public security warnings | Phase 1 |
| Central topology and encrypted server persistence | Phases 1–2 |
| Anonymous workspace, pairing, devices, and recovery | Phase 2 |
| Text revisions, ordering, offline semantics, sync, deletion, and one-year retention | Phases 3–4 |
| Read-only MCP contract and Linux/macOS connector setup | Phase 3 |
| Menu bar UX, thirty-day history, Keychain, SQLite cache, and multi-Mac flow | Phase 4 |
| Static-image normalization, bundle upload, MCP blocks, deletion, and 24-hour expiry | Phase 5 |
| Error paths and unit/integration/end-to-end tests | Every phase, with final cross-system coverage in Phase 6 |
| Single-Droplet topology, automatic `main` deployment, rollback, and release artifacts | Phase 6 |
| Accepted naming, server-trust, single-node, no-backup, recovery, and last-write-wins risks | Phase 1 documentation and Phase 6 operator runbook |

Every section of the approved design maps to at least one phase. The roadmap deliberately defers exact Phase 2–6 file paths and code until the preceding phase establishes the repository interfaces those plans must reference.
