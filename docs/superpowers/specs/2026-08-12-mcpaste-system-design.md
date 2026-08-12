# MCPaste system design

**Status:** Approved

**Date:** 2026-08-12

**Product name:** MCPaste

**Repository target:** `1yoouoo/mcpaste`

## 1. Purpose

MCPaste is a macOS menu bar app for deliberately handing arbitrary text or static images to AI coding tools. The user pastes content into the app, and connected tools such as Codex and Claude Code retrieve the latest valid item through Model Context Protocol (MCP).

The product is a controlled bridge, not a general clipboard manager:

- MCPaste never monitors or automatically saves the system clipboard.
- Only a full macOS app can create, edit, or delete content through the supported product interface.
- MCP clients and headless companion installations are read-only.
- One server-side workspace is the source of truth across every connected device.
- Text, images, device credentials, and logs are handled as sensitive by default.

An existing service already uses the name MCPaste for a Minecraft-focused paste site. The owner has explicitly accepted the naming and search-confusion risk. Trademark, App Store naming, package registry, and domain availability are release prerequisites, not claims made by this document.

## 2. MVP success criteria

The remote-capable MVP is successful when all of the following work end to end:

1. A user installs MCPaste on a Mac, creates a workspace without an email or password, and receives a recovery code.
2. The user deliberately pastes either plain text or one or more supported static images into the app.
3. Codex and Claude Code on that Mac can retrieve the latest valid paste through a local STDIO MCP connector.
4. A Linux machine, including a remote AWS or similar server, can install the headless MCPaste companion, pair with the workspace, and retrieve the same latest paste through Codex or Claude Code.
5. Another Mac can install the full app, pair by QR or short code, and gain the same full app capabilities without becoming a permanent “primary” device.
6. Companion and MCP credentials cannot call create, edit, or delete APIs.
7. Text synchronizes centrally, follows the agreed history and one-year retention rules, and remains readable by MCP when it is the latest valid item.
8. Static images are normalized for AI vision, synchronize centrally, and become unavailable no later than 24 hours after creation.
9. A push to `main` runs required checks and automatically deploys the server to the production Droplet, with health verification and application rollback.
10. Paste bodies, images, authorization material, recovery codes, and pairing codes do not appear in normal logs, tests, documentation examples, or Git history.

## 3. Scope and non-goals

### Included

- Native SwiftUI macOS menu bar app
- Manual text paste and static-image drag-and-drop or paste
- Thirty-day visible history in the macOS app
- Central synchronization across multiple full Mac apps and read-only companions
- Anonymous workspace onboarding, QR/short-code pairing, and recovery codes
- Local STDIO MCP connector for Codex, Claude Code, and compatible clients
- Remote Streamable HTTP MCP endpoint on a single DigitalOcean Droplet
- Linux companion binaries for `amd64` and `arm64`
- Server-side text retention and short-lived image storage
- Public GitHub repository, CI, release artifacts, and automatic production deployment

### Excluded from the MVP

- Windows, iOS, Android, and a general-purpose web app
- Email/password accounts, social login, billing, teams, or workspace sharing between people
- Automatic clipboard monitoring or unlimited clipboard history
- GIF, animated WebP, video, audio, SVG, or PDF ingestion
- End-to-end encryption in which the server cannot decrypt content
- AI write tools, browser-based paste creation, and direct write access from Linux companions
- Full-text search of content older than the visible thirty-day app history
- DigitalOcean managed databases, Spaces, Redis, multi-region deployment, or paid backups
- Automatic app updates; MVP releases are downloaded from GitHub Releases

## 4. Product positioning

MCPaste is intentionally narrower than clipboard-history products that expose an existing clipboard database to MCP. Its differentiators are:

- **Deliberate capture:** nothing is stored until the user explicitly pastes into MCPaste.
- **Remote reach:** a headless AI session on Linux can read content entered on a Mac.
- **One-way AI boundary:** the full app writes; AI connectors read.
- **Secret-oriented simplicity:** the primary flow is “paste, then tell the AI it is ready,” without requiring the user to label environment variables or clean up formatting.

The bridge motif remains part of the iconography and copy even though the product name is MCPaste.

## 5. System architecture

MCPaste uses a monorepo with three deployable components.

### 5.1 macOS app

The SwiftUI menu bar app is the only supported interactive authoring client. It:

- creates or joins a workspace;
- stores full-app credentials in macOS Keychain;
- accepts, previews, normalizes, uploads, edits, and deletes pastes;
- maintains a local SQLite cache and offline write queue;
- displays recent history, device state, sync state, expiry, and MCP access time;
- installs and configures the bundled read-only MCP connector for supported AI clients.

SQLite is a cache, not an independent source of truth. A server acknowledgement is required before another device can see an offline-created item.

### 5.2 MCPaste connector and Linux companion

A single Go executable, `mcpaste`, serves both roles:

- On macOS it is bundled and installed by the app.
- On Linux it is distributed as a standalone companion binary.
- AI clients launch it as a local STDIO MCP server.
- It authenticates to the production Streamable HTTP MCP endpoint and proxies MCP traffic.
- It exposes only read tools and never persists paste bodies or images to disk.
- It is invoked by the MCP client and exits with that session; no background daemon is required.

The connector stores only its revocable read-only credential and non-sensitive connection metadata. On macOS, credentials use Keychain. On Linux, an available system credential store is preferred; otherwise the credential is stored in a user-owned file with mode `0600`.

### 5.3 Central service

A Go service on one DigitalOcean Droplet provides:

- versioned HTTPS APIs for app onboarding, pairing, devices, synchronization, and paste mutations;
- an authenticated Streamable HTTP MCP endpoint for read-only connectors;
- an event stream for near-real-time Mac synchronization;
- PostgreSQL persistence for workspaces, devices, events, encrypted text, and metadata;
- encrypted local-file storage for normalized images;
- retention and image-expiry workers inside the single server process.

Caddy terminates TLS and proxies only the public Go service. PostgreSQL and storage volumes are never exposed publicly. Redis is unnecessary for one service process; durable events in PostgreSQL provide reconnection catch-up.

### 5.4 Request paths

```text
Full Mac app ──HTTPS write/sync──▶ Caddy ──▶ Go service ──▶ PostgreSQL/files
      ▲                                      │
      └────SSE events + incremental sync─────┘

Codex / Claude Code ──STDIO──▶ mcpaste connector
                                  │
                                  └──Streamable HTTP MCP──▶ Caddy ──▶ Go service
```

## 6. User experience

### 6.1 Menu bar surface

Clicking the macOS status item opens a compact bridge-themed popover. The main content surface contains the paste controls rather than a separate page header:

- `Paste`, `+`, and history controls sit together at the upper-right of the paste surface.
- The earlier “새로운 붙여넣기” title and explanatory subtitle do not appear.
- There is no persistent back-arrow control.
- `Paste` imports the current clipboard only after the user clicks it.
- `+` clears the composition surface for a new item and leaves the previous item in history.
- The history icon opens recent records; the history view does not repeat the history icon.
- Selecting a history item opens it. Creating a new item from history uses `+`.

The popover shows sync state and the latest MCP access timestamp down to seconds. The UI never previews secret content in notifications or menu-bar labels.

### 6.2 Text entry

Text is stored exactly as pasted, including line breaks and surrounding whitespace. MCPaste does not require a variable name, infer a schema, redact tokens, or rewrite formatting. Editing an existing text item creates a new immutable server revision.

### 6.3 Image entry

The app accepts image drag-and-drop, clipboard paste, and multiple selection. Supported sources are JPEG, PNG, HEIC/HEIF, static WebP, TIFF, BMP, and other static raster data that macOS ImageIO can decode.

Normalization rules are:

- animated formats, video, SVG, and PDF are rejected with a clear per-item explanation;
- EXIF, GPS, camera, and other unnecessary metadata are stripped;
- alpha-bearing images become PNG; other images become JPEG;
- images larger than a 2048-pixel long edge are downscaled while preserving aspect ratio;
- JPEG starts at quality 82 and is reduced only as needed to remain below 4 MiB per normalized image;
- PNG is resized as needed to remain below 8 MiB;
- the app uses bounded ImageIO thumbnail decoding rather than fully decoding oversized sources into unbounded memory;
- the complete normalized bundle must remain below 32 MiB; the app adaptively reduces quality and dimensions, but never below a 768-pixel long edge, before reporting that an extreme bundle must be split;
- a source file may be up to 250 MiB, and one paste may contain up to 20 images;
- an image that cannot be decoded or normalized within those safety limits fails individually without discarding the other selected images.

The UI presents normalization as automatic optimization, not as a format-conversion workflow. An image bundle becomes the latest paste only after every accepted image has uploaded successfully. Until then, the previous server paste remains current.

## 7. Identity, onboarding, and device pairing

### 7.1 Workspace identity

The MVP has no conventional account. A workspace is an anonymous server-side identity containing devices and one authoritative paste timeline.

On first app launch, the user chooses:

- **New user:** create a new workspace, register the Mac as a full device, and display a recovery code once.
- **Existing user:** display a pairing QR code and short code for approval from an already connected full Mac.

Losing every full Mac and the current recovery code makes the workspace unrecoverable. Read-only companions cannot approve new devices.

### 7.2 Pairing

A joining device creates an unauthenticated, rate-limited pairing request. Its QR and human-readable short code identify only that pending request; they contain no bearer credential. The request expires after five minutes.

An existing full app displays the requesting device’s proposed name, platform, and requested scope, then approves it. The server returns a new random 256-bit device credential only to the joining device. The server stores a cryptographic hash of the credential, not the credential itself.

### 7.3 Device roles

- **Full Mac app:** create, edit, delete, synchronize, approve devices, rename devices, and revoke devices.
- **MCP connector/companion:** retrieve the latest valid paste and report access metadata only.

A full Mac installation receives separate full-app and read-only connector credentials so an AI client never inherits app write authority.

Every device has an immutable globally unique internal ID. Its display name is derived from the local hostname. Display names need only be unique within one workspace; collisions receive suffixes such as `MacBook Pro (2)`. A full app may rename devices using the same workspace-local uniqueness rule.

### 7.4 Recovery

The recovery code is high entropy, shown once, and stored by the server only as an Argon2id verifier. Entering it on a new Mac adds that Mac as another full device, leaves every existing device connected, rotates the recovery code, and notifies connected full apps.

Recovery attempts are rate-limited. Recovery does not reset or replace the workspace. A user wanting a clean start creates a new workspace instead.

## 8. Paste model and synchronization

### 8.1 Paste types

A paste is exactly one of:

- `text`, containing one or more immutable text revisions; or
- `image_bundle`, containing one or more normalized static image assets.

Mixed text captions and images in one paste are outside the MVP.

### 8.2 Server authority and ordering

PostgreSQL is authoritative. Every accepted mutation receives a server sequence number and server receipt timestamp. The latest accepted sequence wins; client clocks do not determine ordering.

This deliberately simple last-write-wins behavior applies to simultaneous edits and offline queues. If an older offline edit reaches the server later, it becomes the visible revision. The previous text revision remains encrypted under the retention policy, but the UI does not present a conflict-resolution flow.

Every client mutation carries an idempotency ID. Repeating a request after a timeout cannot create a duplicate paste or revision.

### 8.3 Incremental synchronization

The server records a durable workspace event for each relevant mutation. A running Mac app receives lightweight invalidations over Server-Sent Events, then requests changes after its last durable cursor. If the event stream is interrupted, the app polls every 15 seconds until it reconnects. A device that was offline resumes from its cursor; if the cursor is no longer available, it performs a fresh thirty-day snapshot.

The Linux companion does not run a sync loop. Each MCP call asks the central service for the current latest valid paste.

### 8.4 Offline behavior

A full Mac app may create or edit text and queue it locally while offline. It displays `Sync pending` until the server acknowledges the mutation. Remote devices continue to see the previous server value.

Image normalization may happen offline, but the bundle is not published until all files upload and the server atomically commits it. The MCP connector has no stale-content fallback: if the service is unavailable, it returns a clear unavailable error rather than an old secret.

## 9. History, deletion, and retention

### 9.1 Text

- The app displays the current form of text pastes created within the most recent 30 days.
- The server retains each text revision for one year from that revision’s server creation time.
- Editing hides the old revision from normal history but does not shorten its one-year retention.
- Deleting a text paste immediately hides it from every online app and MCP response by creating a tombstone.
- The encrypted text body and audit metadata remain inaccessible but retained until their one-year deadlines, then are permanently purged.
- An offline app applies the tombstone during its next sync and removes cached plaintext.
- `get_latest_paste` may return a non-deleted text paste older than 30 days when it remains the latest valid item, up to its one-year expiry.

### 9.2 Images

- Every image bundle expires 24 hours after server creation.
- Authorization checks enforce expiry immediately even if a cleanup worker has not yet run.
- A cleanup worker runs at least once per minute and permanently removes expired files, thumbnails, and image metadata.
- Explicit user deletion permanently removes the image files and metadata immediately.
- Images are not included in backups and are never retained for one year.
- When the current image expires or is deleted, “latest” falls back to the next newest non-deleted, non-expired paste.

## 10. MCP contract

The MVP exposes one read-only tool:

### `get_latest_paste`

It takes no content selector. It returns the newest non-deleted and non-expired paste for the connector’s workspace.

For text, the result contains metadata plus one text content block preserving the exact stored text. For an image bundle, it contains metadata plus one MCP image content block per normalized image, preserving bundle order. If the workspace has no valid item, the tool returns a structured empty result rather than an error.

Every successful retrieval atomically updates the paste’s last MCP access time and access count. Full apps receive that metadata through synchronization and display the timestamp to seconds.

The tool does not search history, list credentials, mutate content, reveal deleted revisions, or expose another workspace. Server authorization is based on the connector credential, never on whether the caller is Codex, Claude Code, or another MCP client.

The open-source nature of the client means MCPaste does not attempt DRM against a workspace owner modifying their own app. The enforceable guarantee is that server-issued companion and MCP credentials are read-only and that no supported MCP method performs writes.

## 11. Security model

### 11.1 Trust boundary

MCPaste is not end-to-end encrypted. The server must decrypt content to return it to authorized devices. TLS protects data in transit, and application encryption protects stored bodies from casual database or volume disclosure. A server operator with the master key, or an attacker with full Droplet access, can read stored content.

The UI and documentation must state this plainly. MCPaste also cannot control how Codex, Claude Code, model providers, terminal logging, or user prompts retain content after MCP returns it.

### 11.2 Encryption and credentials

- TLS is mandatory for every public request.
- Text bodies and image files use authenticated AES-256-GCM encryption with a unique random nonce per object.
- The production master encryption key exists only in `/etc/mcpaste/server.env` on the Droplet, owned by root with mode `0600`.
- Database passwords and production session secrets live in the same server-only environment file.
- The master key is never stored in the repository, macOS app, GitHub Actions secrets, container image, URL, or log.
- Device bearer credentials are random 256-bit values and are stored server-side only as hashes.
- Recovery codes use Argon2id verification.
- Pairing and recovery endpoints use IP- and request-based rate limits implemented in PostgreSQL/in-process state; Redis is not required for the single-node MVP.

Key rotation is an operator maintenance procedure after the MVP; the initial format must store a key identifier with every ciphertext so a later online re-encryption migration is possible.

### 11.3 Logging and telemetry

Application, connector, Caddy, CI, and test logs may include timestamps, request IDs, status codes, durations, object IDs, sizes, and counts. They must not include:

- paste text or image bytes;
- authorization headers, cookies, device credentials, or recovery codes;
- pairing short codes or QR payloads;
- secret-bearing URLs or request/response bodies;
- local Keychain values or credential-file contents.

The MVP has no third-party product analytics. Panic and error handling must redact request bodies by construction, not by best-effort pattern matching.

### 11.4 Public repository controls

- Examples and fixtures use visibly fake deterministic values.
- `.env*`, databases, local credentials, build outputs, signing files, and app data remain ignored.
- CI runs secret scanning and dependency/vulnerability checks.
- GitHub Actions use minimal permissions, pinned action revisions, and no production secrets for forked pull requests.
- Security reports use GitHub private vulnerability reporting rather than public issues.

## 12. Installation and AI-native setup

### 12.1 macOS

The user downloads a signed and notarized DMG from GitHub Releases, moves MCPaste to Applications, and follows the in-app new/existing-user funnel. The app detects supported AI clients and installs the bundled connector configuration with explicit confirmation. It edits only its named MCP entry, preserves unrelated configuration, writes atomically, and keeps a local backup before changing an existing file.

The app supports macOS 14 or later on Apple Silicon and Intel. Release verification covers the minimum supported and current macOS versions.

### 12.2 Linux and remote servers

GitHub Releases publish static Go binaries and SHA-256 checksums for Linux `amd64` and `arm64`. The README is written so either a person or a coding agent can:

1. identify the architecture;
2. download the matching release artifact and checksum;
3. verify the checksum;
4. install `mcpaste` in a user-controlled executable path;
5. run the interactive setup immediately;
6. display a QR and short code for approval by a full Mac app;
7. detect Codex and Claude Code and add only the MCPaste configuration entry.

There is no separate `mcpaste connect` command. A normal interactive installer launches setup. If installation was non-interactive or setup was skipped, the recovery entry point is `mcpaste setup`. Configuration writes are idempotent and never overwrite unrelated MCP servers.

## 13. Server deployment and operations

### 13.1 Production topology

The owner creates one DigitalOcean Droplet when the server is ready to deploy. The baseline is Ubuntu 24.04 LTS, 1 vCPU, 1 GiB RAM, and persistent local disk. If observed memory pressure makes that unsafe, vertical resizing is the first scaling action.

Docker Compose runs:

- Caddy;
- the Go MCPaste service;
- PostgreSQL.

Only ports 80 and 443 are public. SSH port 22 is restricted to the owner’s trusted source where practical. PostgreSQL has no public port. Caddy obtains and renews TLS certificates after the owner points a chosen domain’s DNS record to the Droplet.

No DigitalOcean backup, managed database, object storage, or off-Droplet backup is part of the MVP. Droplet deletion or disk failure can therefore cause irreversible loss of all workspaces and retained text. This is an explicitly accepted cost-saving risk, not a durability guarantee.

### 13.2 Continuous integration and deployment

Pull requests run formatting, static analysis, unit tests, integration tests, secret scanning, dependency checks, and build verification without deployment credentials.

Every push to `main`, including a merged pull request, runs the same required checks and then:

1. builds an immutable server image;
2. pushes it to GitHub Container Registry by digest;
3. connects to the Droplet using a restricted deploy credential;
4. pulls the new image and runs forward-compatible database migrations;
5. starts the new application container;
6. verifies readiness and an authenticated smoke path;
7. records success or restores the previous application image on failure.

Database migrations follow expand-and-contract rules so application rollback remains possible. A failed migration stops deployment before the old application is replaced. Destructive schema contraction is deferred until no released application depends on the old shape.

The GitHub `production` environment stores only deployment host/user/key and known-host data. It has no manual reviewer because `main` deployment is intentionally automatic. The server master encryption key and database credentials never pass through GitHub Actions.

Version tags create GitHub Releases containing the signed/notarized macOS artifact, Linux binaries, checksums, and release notes. Tags do not perform a second production deployment when the tagged commit has already passed through `main`.

### 13.3 Owner prerequisites

Before the first production deployment, the owner must provide:

- the Droplet and SSH key;
- a domain and DNS record;
- production GitHub environment values for restricted SSH deployment;
- an Apple Developer Program membership, Developer ID signing certificate, and notarization credentials for public macOS distribution.

These are external prerequisites. Their actual secret values must never be added to this design document or the public repository.

## 14. Error handling

- **Service unavailable:** the app keeps acknowledged cache data visible and marks unsynchronized writes; MCP returns unavailable and no stale body.
- **Expired or revoked credential:** the connector reports that pairing is required and does not expose cached content.
- **Partial image upload:** the server discards uncommitted temporary objects; the previous paste remains latest.
- **Expired image:** every read path rejects it even before physical cleanup, then falls back to the next valid paste.
- **Duplicate mutation:** the idempotency record returns the original result.
- **Concurrent edit:** server sequence order decides; no conflict dialog appears.
- **Damaged ciphertext or missing file:** the server returns an unavailable-content error, records metadata-only diagnostics, and never returns partial plaintext.
- **Configuration edit failure:** setup restores the previous client configuration and prints manual instructions without exposing credentials.
- **Failed deployment:** the old healthy application remains or is restored; the workflow reports the exact failed health or migration step.

## 15. Testing and release gates

### 15.1 Unit and contract tests

- scope authorization and cross-workspace isolation;
- pairing expiry, approval, rate limits, and credential hashing;
- recovery rotation without revoking existing devices;
- server-sequence last-write-wins and idempotency;
- text revision, tombstone, one-year retention, and thirty-day history filtering;
- image format detection, metadata stripping, normalization, 24-hour expiry, and deletion;
- encryption round trips, nonce uniqueness, key identifiers, and corruption handling;
- log redaction and secret fixture scanning;
- MCP text/image/empty result contracts;
- Codex and Claude Code configuration updates that preserve unrelated entries.

### 15.2 Integration and end-to-end tests

- first Mac creates a workspace and pastes text;
- local Codex and Claude Code retrieve it;
- Linux `amd64` and `arm64` companions pair and retrieve it remotely;
- a second full Mac pairs, writes, receives updates, and renames devices;
- offline Mac writes synchronize later under server-order semantics;
- multiple images normalize, upload atomically, and reach MCP image blocks;
- text deletion disappears immediately while retained ciphertext stays inaccessible;
- image deletion and 24-hour expiry physically remove assets;
- recovery adds a new full Mac, rotates the code, and preserves old devices;
- server restart preserves PostgreSQL and file data;
- a deliberately unhealthy server container image exercises automatic application rollback.

### 15.3 Release gates

A release cannot be called verified unless:

- all required CI checks pass from clean checkout;
- signed artifact and checksum verification passes;
- real current Codex and Claude Code clients complete the smoke flow;
- no test or sample contains a real secret;
- production readiness and MCP smoke checks pass after `main` deployment.

## 16. Repository layout

The implementation plan should converge on this minimal structure without introducing unused frameworks:

```text
apps/macos/                 SwiftUI menu bar app
cmd/mcpaste/                Go STDIO connector and Linux setup CLI
cmd/server/                 Go API, remote MCP, sync, and workers
internal/                   Focused Go packages shared by server/connector as needed
api/                        Versioned HTTP and MCP contract definitions
db/migrations/              PostgreSQL migrations
deploy/                     Docker Compose, Caddy, and bootstrap documentation
docs/                       Architecture, security, operations, and contributor docs
.github/workflows/          Pull-request CI, main deployment, and tagged releases
```

Existing `Paste Bridge` documents describe an obsolete local-only plan. The first implementation task must rename the project to MCPaste and reconcile `README.md`, `SECURITY.md`, `.env.example`, `.gitignore`, and `docs/security-and-secrets.md` with this specification before code is added.

## 17. Known risks and accepted tradeoffs

- The chosen name collides with an unrelated active paste service and still requires release-time legal and store review.
- Server-side decryption makes the service operator and a full Droplet compromise part of the trust boundary.
- One low-cost Droplet is a single point of failure.
- No backup means central text can be lost despite the one-year retention policy.
- Anonymous recovery has no support-assisted identity proof; loss of devices and recovery code is final.
- Last-write-wins can surface an older offline edit as the newest value.
- Client and model-provider retention begins after MCP returns content and is outside MCPaste’s control.
- Image normalization prioritizes broad acceptance and AI readability over archival quality.

These are deliberate MVP choices. Changing any of them requires a design revision rather than an incidental implementation decision.
