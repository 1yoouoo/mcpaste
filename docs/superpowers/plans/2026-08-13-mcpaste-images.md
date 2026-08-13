# MCPaste static image flow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Add normalized static-image bundles with encrypted file storage, atomic publication, 24-hour expiry, explicit deletion, HTTP retrieval, synchronization metadata, and MCP image content blocks.

**Architecture:** A `image_bundle` revision shares the existing workspace sequence and paste timeline. Asset bytes are encrypted with AES-256-GCM and stored below a configured data directory; PostgreSQL stores only asset metadata and envelope identifiers. The server validates expiry on every read and the cleanup worker removes expired files and metadata.

**Tech Stack:** Go 1.26.5, PostgreSQL 18.4, pgx, AES-256-GCM, ImageIO-normalized JPEG/PNG input, multipart HTTP, official Go MCP SDK.

**Spec:** `docs/superpowers/specs/2026-08-12-mcpaste-system-design.md`

## Global Constraints

- Supported formats are JPEG, PNG, HEIC/HEIF, static WebP, TIFF, BMP, and other static raster data decodable by ImageIO.
- One source image is at most 250 MiB and one bundle has at most 20 images.
- The server stores normalized image bytes only after every asset validates and uploads successfully.
- Every image bundle expires 24 hours after server creation and every read path enforces expiry before cleanup.
- Explicit deletion removes files and metadata immediately; expiry fallback selects the next valid paste.
- Image bytes are encrypted with a unique AES-GCM nonce and never appear in logs, SSE, sync metadata, or Git.
- Connector credentials cannot call image upload, image download, delete, or any full-device API.

---

## Task 1: Add image schema and storage configuration

**Files:**

- Create `db/migrations/000003_image_bundles.up.sql` and `.down.sql`.
- Modify `internal/config/config.go`, `internal/config/config_test.go`, `.env.example`, and `compose.yaml`.
- Create `internal/images/storage.go` and `internal/images/storage_test.go`.

- [ ] Step 1: Write migration/config tests for `pastes.paste_kind`, image revisions, asset metadata, expiry checks, and `MCPASTE_DATA_DIR` path validation.
- [ ] Step 2: Run tests red.
- [ ] Step 3: Add `image_bundle` event/revision types, `paste_assets` metadata, composite foreign keys, 24-hour expiry constraints, and indexes for expiry/sequence.
- [ ] Step 4: Add a data directory default suitable for development and require an explicit non-empty path in production. Implement safe workspace/paste/revision/asset paths that reject traversal and symlinked parents.
- [ ] Step 5: Implement `FileStore.Put`, `Open`, `Remove`, and `RemoveTree` with mode 0700 directories, exclusive temporary files, fsync, atomic rename, and no path/value leakage in errors.
- [ ] Step 6: Run migration, storage, and config tests and commit `feat: add image storage boundary`.

## Task 2: Implement image normalization and multipart validation

**Files:**

- Create `internal/images/metadata.go` and `internal/images/metadata_test.go`.
- Create `internal/identity/postgres/images.go`.
- Modify `internal/identity/model.go`, `dto.go`, `store.go`, and `service.go`.
- Create `internal/identity/image.go` and `image_test.go`.

- [ ] Step 1: Write tests for accepted metadata, unsupported/animated rejection, 20-image and 250 MiB limits, all-or-nothing failure, encrypted file bytes, and 24-hour expiry.
- [ ] Step 2: Run tests red.
- [ ] Step 3: Define `ImageAsset`, `ImageBundle`, `LatestPaste.Images`, and image-specific repository methods. Keep image bytes out of normal text DTOs and sync event bodies.
- [ ] Step 4: Implement bounded multipart parsing with content-type allowlisting, byte counters, dimension checks, and normalized metadata supplied by the macOS ImageIO client. Reject animated formats and malformed headers before persistence.
- [ ] Step 5: In one transaction, allocate the workspace sequence, insert the image revision and asset metadata, encrypt/write all files, insert one event, and publish only after every file is durable. On any error, remove temporary files and roll back metadata.
- [ ] Step 6: Add image download authorization for full scope, expiry checks, range rejection, and generic unavailable errors. Commit `feat: add image bundle persistence`.

## Task 3: Extend HTTP sync, latest selection, deletion, and cleanup

**Files:**

- Modify `internal/httpserver/api.go`, `pastes.go`, `sync.go`, `mcp.go`, and `internal/identity/postgres/cleanup.go`.
- Modify `internal/identity/postgres/pastes.go` and `sync.go`.
- Modify `internal/httpserver/identity_integration_test.go` and add `image_integration_test.go`.

- [ ] Step 1: Write tests for image upload, metadata-only history/sync, full-only download, delete, expiry fallback, and connector rejection.
- [ ] Step 2: Run image HTTP tests red.
- [ ] Step 3: Register `POST /v1/image-pastes`, `GET /v1/image-pastes/{paste_id}/{asset_index}`, and reuse `DELETE /v1/pastes/{paste_id}` for full-scope deletion. Keep method guard complete.
- [ ] Step 4: Make latest selection consider text and image bundles by server sequence, skip tombstones and expired images, and update access metadata atomically.
- [ ] Step 5: Make sync/SSE return only image revision metadata; never include bytes. Add cleanup every minute for expired image files and rows.
- [ ] Step 6: Run all image integration tests, race tests for image paths, and commit `feat: expose image bundle api`.

## Task 4: Add MCP image content blocks

**Files:**

- Modify `internal/mcpserver/server.go` and `server_test.go`.
- Modify `internal/identity/model.go` and service image retrieval types.
- Add image cases to `internal/httpserver/identity_integration_test.go`.

- [ ] Step 1: Write official SDK client tests for ordered image blocks, metadata, empty fallback, expiry fallback, and corrupt-file unavailable behavior.
- [ ] Step 2: Run tests red.
- [ ] Step 3: Map every valid normalized asset to `mcp.ImageContent` with MIME type and bytes in bundle order. Do not return stale image data after expiry or deletion.
- [ ] Step 4: Run MCP, HTTP, and identity tests and commit `feat: return image bundles through mcp`.

## Task 5: Add macOS ImageIO normalization and UI flow

**Files:**

- Create `macos/MCPaste/Sources/MCPasteCore/ImageNormalizer.swift`.
- Create `macos/MCPaste/Tests/MCPasteCoreTests/ImageNormalizerTests.swift`.
- Modify Phase 4 upload/session views and API models.

- [ ] Step 1: Write fixture tests for JPEG, PNG, HEIC if available, animated rejection, metadata stripping, dimensions, and bundle ordering.
- [ ] Step 2: Run tests red.
- [ ] Step 3: Implement bounded ImageIO decode, metadata stripping, normalized JPEG/PNG encoding, size checks, and temporary-file cleanup.
- [ ] Step 4: Add drag/drop, explicit clipboard paste, multi-image preview, per-item errors, atomic upload state, and previous-paste preservation until server acknowledgement.
- [ ] Step 5: Run Swift tests and app build and commit `feat: add macos image normalization flow`.

## Task 6: Validate Phase 5 and record the handoff

- [ ] Step 1: Run Go race/vet/test/build checks and image-specific PostgreSQL integration tests.
- [ ] Step 2: Run Swift tests with image fixtures and the macOS release build.
- [ ] Step 3: Run Gitleaks and inspect that the data directory, normalized bytes, and test fixtures are ignored/not tracked.
- [ ] Step 4: Create `docs/superpowers/records/2026-08-13-mcpaste-images.md` and commit `docs: record image phase handoff`.

