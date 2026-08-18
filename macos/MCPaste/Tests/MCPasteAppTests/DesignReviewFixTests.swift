import XCTest
import SwiftUI
import MCPasteCore
@testable import MCPasteApp

/// A workspace API that records paste writes; everything else fails loudly.
private final class RecordingWorkspaceAPI: WorkspaceAPI {
    var createdTexts: [String] = []
    var updatedTexts: [(id: String, text: String)] = []
    private var nextSequence: Int64 = 10

    func createPaste(text: String, idempotencyKey: String) async throws -> PasteRecord {
        createdTexts.append(text)
        nextSequence += 1
        return PasteRecord(id: "created-\(createdTexts.count)", revisionID: "r\(nextSequence)", sequence: nextSequence, text: text, deleted: false, expiresAt: Date().addingTimeInterval(3600))
    }

    func updatePaste(id: String, text: String, idempotencyKey: String) async throws -> PasteRecord {
        updatedTexts.append((id, text))
        nextSequence += 1
        return PasteRecord(id: id, revisionID: "r\(nextSequence)", sequence: nextSequence, text: text, deleted: false, expiresAt: Date().addingTimeInterval(3600))
    }

    func deletePaste(id: String, idempotencyKey: String) async throws {}
    func replaceAttachments(pasteID: String, images: [NormalizedImage], idempotencyKey: String) async throws -> PasteRecord { throw APIError.invalidResponse }
    func downloadAttachment(pasteID: String, assetIndex: Int) async throws -> Data { throw APIError.invalidResponse }
    func sync(after: Int64) async throws -> SyncPage { SyncPage(cursor: after, hasMore: false, events: []) }
    func snapshot() async throws -> SnapshotPage { throw APIError.invalidResponse }
    func eventStream(after: Int64) -> AsyncThrowingStream<Int64, Error> {
        AsyncThrowingStream { $0.finish() }
    }
}

@MainActor
final class DesignReviewFixTests: XCTestCase {
    private func isolatedKeychain() -> KeychainStore {
        KeychainStore(service: "com.mcpaste.tests.\(UUID().uuidString)")
    }

    private func paste(_ id: String, sequence: Int64, text: String? = "text") -> CachedPaste {
        CachedPaste(id: id, revisionID: "r-\(id)", sequence: sequence, text: text, deleted: false, expiresAt: Date().addingTimeInterval(3600))
    }

    // MARK: Fix 1 — opening the content window activates the app and closes the popover

    func testOpeningContentWindowActivatesAndDismissesInOrder() {
        var calls: [String] = []

        ContentWindowOpener.open(
            openWindow: { calls.append("open") },
            activate: { calls.append("activate") },
            dismissPopover: { calls.append("dismiss") }
        )

        XCTAssertEqual(calls, ["open", "activate", "dismiss"],
                       "the window must exist before activation brings it forward, and the popover closes last")
    }

    func testAutoOpenActivatesEvenWithoutAPopoverToDismiss() {
        var calls: [String] = []

        ContentWindowOpener.open(
            openWindow: { calls.append("open") },
            activate: { calls.append("activate") }
        )

        XCTAssertEqual(calls, ["open", "activate"])
    }

    // MARK: Fix 2 — switching pastes commits the draft instead of discarding it

    func testSelectingAnotherPasteCommitsTheUnsavedDraftFirst() async {
        let model = AppModel(keychain: isolatedKeychain())
        let api = RecordingWorkspaceAPI()
        model.installAPIForTesting(api)
        let other = paste("p-other", sequence: 3, text: "older words")
        model.previewSeed(draft: "unsaved words", history: [other])

        await model.selectPaste(other)

        XCTAssertEqual(api.createdTexts, ["unsaved words"], "the edit in progress must reach the server before it leaves the screen")
        XCTAssertEqual(model.draft, "older words", "the clicked paste still opens afterwards")
    }

    func testReselectingTheOpenPasteKeepsEditsInProgress() async {
        let model = AppModel(keychain: isolatedKeychain())
        let api = RecordingWorkspaceAPI()
        model.installAPIForTesting(api)
        let open = paste("p-open", sequence: 3, text: "stored text")
        model.previewSeed(draft: "stored text edited", history: [open], selectedPasteID: "p-open")

        await model.selectPaste(open)

        XCTAssertEqual(model.draft, "stored text edited", "clicking the row of the open paste must not reload it over live edits")
        XCTAssertTrue(api.createdTexts.isEmpty)
        XCTAssertTrue(api.updatedTexts.isEmpty)
    }

    // MARK: Fix 3 — "Last synced" stays current

    func testLastSyncedLabelFollowsTheClock() {
        let synced = Date(timeIntervalSince1970: 1_000_000)

        XCTAssertEqual(lastSyncedLabel(for: synced, now: synced.addingTimeInterval(2)), "just now")
        XCTAssertEqual(lastSyncedLabel(for: synced, now: synced.addingTimeInterval(20)), "20 seconds ago")
        XCTAssertEqual(lastSyncedLabel(for: synced, now: synced.addingTimeInterval(660)), "11 minutes ago",
                       "the same sync time must render differently as the clock advances")
        XCTAssertEqual(lastSyncedLabel(for: nil, now: synced), "Not yet")
    }

    // MARK: Fix 4 (revised) — status reports the connection and MCP, never nags about saving

    func testPillReflectsOnlyTheConnection() {
        XCTAssertEqual(EditorStatus.pill(syncFailed: true, syncing: false, connected: true).text, "Offline")
        XCTAssertEqual(EditorStatus.pill(syncFailed: false, syncing: true, connected: true).text, "Syncing")
        XCTAssertEqual(EditorStatus.pill(syncFailed: false, syncing: false, connected: true).text, "Online")
        XCTAssertEqual(EditorStatus.pill(syncFailed: false, syncing: false, connected: false).text, "Connecting")
    }

    func testNewPastePromisesAutoSaveInsteadOfUnsavedWarnings() {
        let subtitle = EditorStatus.headerSubtitle(hasSavedSelection: false, lastSyncedAt: nil, now: Date())
        XCTAssertEqual(subtitle, "Saves automatically", "everything saves on its own — no unsaved nag")
    }

    func testSavedSelectionReportsSyncedTime() {
        let now = Date()
        let subtitle = EditorStatus.headerSubtitle(hasSavedSelection: true, lastSyncedAt: now.addingTimeInterval(-30), now: now)
        XCTAssertEqual(subtitle, "Synced 30 seconds ago")
    }

    func testFooterCountsLinkedMCPConnectors() {
        XCTAssertEqual(EditorStatus.footer(saving: false, connectorCount: 0).text, "No MCP connector linked yet")
        XCTAssertEqual(EditorStatus.footer(saving: false, connectorCount: 1).text, "1 MCP connector linked")
        XCTAssertEqual(EditorStatus.footer(saving: false, connectorCount: 3).text, "3 MCP connectors linked")
        XCTAssertEqual(EditorStatus.footer(saving: true, connectorCount: 1).text, "Saving…",
                       "an in-flight save still gets brief feedback")
    }

    func testMCPConnectorCountIgnoresFullDevices() {
        let mac = DeviceRecord(id: "d1", displayName: "This Mac", platform: "macos", role: "full", isCurrent: true)
        let connector = DeviceRecord(id: "d2", displayName: "claude-code", platform: "linux", role: "connector", isCurrent: false)

        XCTAssertEqual(AppModel.mcpConnectorCount(in: [mac]), 0)
        XCTAssertEqual(AppModel.mcpConnectorCount(in: [mac, connector]), 1)
    }

    // MARK: Fix 5 — sidebar empty state no longer points at a removed Save button

    func testEmptyHistoryCopyDescribesAutoSave() {
        XCTAssertFalse(EditorStatus.emptyHistory.contains("press Save"), "the Save button no longer exists")
        XCTAssertTrue(EditorStatus.emptyHistory.contains("saves automatically"))
    }

    // MARK: Fix 6 — one device-count phrase everywhere

    func testDeviceCountLabelCountsCompanionsNotRows() {
        let thisMac = DeviceRecord(id: "d1", displayName: "This Mac", platform: "macOS", role: "full", isCurrent: true)
        let linux = DeviceRecord(id: "d2", displayName: "linux", platform: "linux", role: "read-only", isCurrent: false)
        let cli = DeviceRecord(id: "d3", displayName: "cli", platform: "linux", role: "read-only", isCurrent: false)

        XCTAssertEqual(AppModel.deviceCountLabel(for: []), "This Mac only")
        XCTAssertEqual(AppModel.deviceCountLabel(for: [thisMac]), "This Mac only",
                       "a list that contains only this Mac must not read as a connected companion")
        XCTAssertEqual(AppModel.deviceCountLabel(for: [thisMac, linux]), "This Mac + 1 device")
        XCTAssertEqual(AppModel.deviceCountLabel(for: [thisMac, linux, cli]), "This Mac + 2 devices")
    }

    // MARK: Fix 7 — paste numbers start at #1 and stay dense

    func testDisplayNumbersAreDenseFromOneDespiteSequenceGaps() {
        // Server sequences 2, 5, 9: workspace events (device setup, edits) consumed the rest.
        let history = [paste("c", sequence: 9), paste("b", sequence: 5), paste("a", sequence: 2)]

        XCTAssertEqual(AppModel.displayNumber(for: history[2], in: history), 1, "the first paste is #1, never #2")
        XCTAssertEqual(AppModel.displayNumber(for: history[1], in: history), 2)
        XCTAssertEqual(AppModel.displayNumber(for: history[0], in: history), 3)
    }
}
