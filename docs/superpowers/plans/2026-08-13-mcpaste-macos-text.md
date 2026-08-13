# MCPaste macOS text app Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Build the macOS 14 SwiftUI menu-bar client for workspace onboarding, text authoring, Keychain credentials, SQLite caching, offline mutations, synchronization, and device controls.

**Architecture:** Keep all HTTP, authentication, persistence, and sync behavior in a testable `MCPasteCore` Swift library. Keep the executable target thin: it owns `MenuBarExtra`, presentation state, and user confirmation. SQLite is a cache and queue; the Go service remains authoritative.

**Tech Stack:** Swift 5.10+, macOS 14+, SwiftUI, Foundation, Security Keychain Services, SQLite3, URLSession, XCTest, Swift Package Manager.

**Spec:** `docs/superpowers/specs/2026-08-12-mcpaste-system-design.md`

## Global Constraints

- The app supports macOS 14 or later on Intel and Apple Silicon.
- Paste text is preserved exactly, including line endings and surrounding whitespace.
- Full credentials are stored in Keychain; connector credentials are separate and read-only.
- SQLite is a cache and offline queue, never an independent source of truth.
- Offline mutations remain visibly pending until server acknowledgement.
- The app never logs paste bodies, credentials, pairing secrets, or configuration contents.
- Configuration writes are explicit, atomic, idempotent, and preserve unrelated entries.
- No image implementation belongs in this plan; image behavior is Phase 5.

---

## Task 1: Create the Swift package and test harness

**Files:**

- Create `macos/MCPaste/Package.swift`.
- Create `macos/MCPaste/Sources/MCPasteCore/Placeholder.swift`.
- Create `macos/MCPaste/Tests/MCPasteCoreTests/PackageSmokeTests.swift`.
- Modify `.gitignore` only if the existing Swift/Xcode ignores do not cover package output.

- [ ] Step 1: Write the package smoke test.

```swift
import XCTest
@testable import MCPasteCore

final class PackageSmokeTests: XCTestCase {
    func testPackageLoads() {
        XCTAssertEqual(MCPasteCoreVersion.current, "0.1.0")
    }
}
```

- [ ] Step 2: Run `swift test` and verify the new test fails because the package does not exist.
- [ ] Step 3: Add a macOS 14 library target and the `MCPasteCoreVersion` value.
- [ ] Step 4: Run `swift test` and `swift build -c release`; both must pass.
- [ ] Step 5: Commit `feat: scaffold macos client package`.

## Task 2: Add API models, authenticated client, and Keychain store

**Files:**

- Create `macos/MCPaste/Sources/MCPasteCore/APIModels.swift`.
- Create `macos/MCPaste/Sources/MCPasteCore/MCPasteAPI.swift`.
- Create `macos/MCPaste/Sources/MCPasteCore/KeychainStore.swift`.
- Create `macos/MCPaste/Tests/MCPasteCoreTests/APIClientTests.swift`.
- Create `macos/MCPaste/Tests/MCPasteCoreTests/KeychainStoreTests.swift`.

- [ ] Step 1: Write URLProtocol tests for workspace creation, text mutation, sync, device listing, and 401/403 decoding. Assert Authorization and Idempotency-Key headers are present and request bodies are not logged.
- [ ] Step 2: Run the focused tests red.
- [ ] Step 3: Implement `APIClient` with `URLSession`, one bearer header, JSON decoding, strict HTTP status mapping, and methods `createWorkspace`, `createPaste`, `updatePaste`, `deletePaste`, `listPastes`, `sync`, `listDevices`, `renameDevice`, and `revokeDevice`.
- [ ] Step 4: Implement `KeychainStore` using `kSecClassGenericPassword`, service `com.mcpaste.credentials`, account-scoped workspace/device records, and delete/replace operations without returning secret values in errors.
- [ ] Step 5: Run `swift test --filter 'APIClientTests|KeychainStoreTests'` and commit `feat: add macos api and keychain client`.

## Task 3: Add SQLite cache and offline mutation queue

**Files:**

- Create `macos/MCPaste/Sources/MCPasteCore/SQLiteCache.swift`.
- Create `macos/MCPaste/Sources/MCPasteCore/OfflineQueue.swift`.
- Create `macos/MCPaste/Tests/MCPasteCoreTests/SQLiteCacheTests.swift`.
- Create `macos/MCPaste/Tests/MCPasteCoreTests/OfflineQueueTests.swift`.

- [ ] Step 1: Write tests for schema creation, exact text round-trip, cursor persistence, pending create/update/delete ordering, retry attempts, and tombstone removal of cached plaintext.
- [ ] Step 2: Run the focused tests red.
- [ ] Step 3: Implement a SQLite schema with `metadata`, `paste_revisions`, and `offline_mutations`; use prepared statements, transactions, foreign keys, and WAL mode.
- [ ] Step 4: Implement queue operations that serialize canonical JSON payloads, keep idempotency keys stable across retries, and remove plaintext when a tombstone is applied.
- [ ] Step 5: Run `swift test --filter 'SQLiteCacheTests|OfflineQueueTests'` and commit `feat: add macos sqlite cache and offline queue`.

## Task 4: Implement synchronization and device workflows

**Files:**

- Create `macos/MCPaste/Sources/MCPasteCore/SyncCoordinator.swift`.
- Create `macos/MCPaste/Sources/MCPasteCore/WorkspaceSession.swift`.
- Create `macos/MCPaste/Tests/MCPasteCoreTests/SyncCoordinatorTests.swift`.
- Create `macos/MCPaste/Tests/MCPasteCoreTests/WorkspaceSessionTests.swift`.

- [ ] Step 1: Write tests for initial snapshot, incremental cursor sync, 410 cursor expiry fallback, SSE invalidation followed by sync, 15-second polling fallback, offline queue replay, and device rename/revoke.
- [ ] Step 2: Run the tests red.
- [ ] Step 3: Implement `SyncCoordinator` with an injectable clock and scheduler, durable cursor writes, snapshot replacement, server-order application, and no stale plaintext after deletion.
- [ ] Step 4: Implement `WorkspaceSession` to expose `syncState`, `history`, `pendingCount`, `lastMCPAccessAt`, and device actions to the UI.
- [ ] Step 5: Run `swift test --filter 'SyncCoordinatorTests|WorkspaceSessionTests'` and commit `feat: add macos sync workflows`.

## Task 5: Build the SwiftUI menu-bar app

**Files:**

- Create `macos/MCPaste/Sources/MCPasteApp/MCPasteApp.swift`.
- Create `macos/MCPaste/Sources/MCPasteApp/AppModel.swift`.
- Create `macos/MCPaste/Sources/MCPasteApp/Views/OnboardingView.swift`.
- Create `macos/MCPaste/Sources/MCPasteApp/Views/PastePopoverView.swift`.
- Create `macos/MCPaste/Sources/MCPasteApp/Views/DeviceListView.swift`.
- Create `macos/MCPaste/Tests/MCPasteAppTests/AppModelTests.swift`.

- [ ] Step 1: Write model tests for new workspace, joining, explicit paste, edit, delete confirmation, pending state, and recovery display without logging recovery code.
- [ ] Step 2: Run tests red.
- [ ] Step 3: Implement `MenuBarExtra` with a bridge-themed popover, onboarding states, text editor, recent history, sync status, expiry, MCP access time, device list, and rename/revoke controls.
- [ ] Step 4: Ensure no notification, menu-bar title, or error includes paste text or credentials; add accessibility labels and keyboard submit behavior.
- [ ] Step 5: Run Swift tests, `swift build -c release`, and `xcodebuild -scheme MCPaste -destination 'platform=macOS' build` where the generated scheme is available. Commit `feat: add macos menu bar text app`.

## Task 6: Validate Phase 4 and record the handoff

- [ ] Step 1: Run `swift test`, `swift build -c release`, and the macOS app build from a clean checkout.
- [ ] Step 2: Run Go integration tests against the Compose PostgreSQL service to prove Phase 2/3 behavior remains intact.
- [ ] Step 3: Run a static secret scan over Swift sources and ensure no Keychain values or text fixtures are committed.
- [ ] Step 4: Create `docs/superpowers/records/2026-08-13-mcpaste-macos-text.md` with commands, limitations, and external owner actions.
- [ ] Step 5: Commit `docs: record macos text phase handoff`.

