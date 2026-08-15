import XCTest
import AppKit
import MCPasteCore
@testable import MCPasteApp

@MainActor
final class AppModelTests: XCTestCase {
    func testEmptyModelStartsInOnboarding() {
        XCTAssertEqual(AppScreen.onboarding, AppModel().screen)
    }

    func testEmptyModelDoesNotShowRestoreError() async throws {
        let keychain = KeychainStore(service: "com.mcpaste.tests.\(UUID().uuidString)")
        let model = AppModel(keychain: keychain, restoreOnInit: true)
        try await Task.sleep(nanoseconds: 100_000_000)
        XCTAssertNil(model.errorMessage)
        XCTAssertEqual(model.screen, .onboarding)
    }

    func testOnboardingModelUsesBuildConfiguredEndpointPolicy() throws {
        let configuredEndpoint = try MCPasteEndpoint.baseURL().absoluteString
        XCTAssertTrue(MCPasteEndpoint.matchesConfiguredEndpoint(configuredEndpoint))
        XCTAssertFalse(MCPasteEndpoint.matchesConfiguredEndpoint(configuredEndpoint + "/"))
    }

    func testRecoverySetupConfirmationEntersPasteboard() {
        let model = AppModel(keychain: KeychainStore())
        model.completeRecoverySetup()
        XCTAssertEqual(model.screen, .pasteboard)
    }

    func testSyncStatusStartsNotConnected() {
        let model = AppModel(keychain: isolatedKeychain())
        XCTAssertEqual(model.syncStatus, .notConnected)
    }

    func testDeleteIsUnavailableWithoutSavedPaste() async {
        let model = AppModel(keychain: isolatedKeychain())

        XCTAssertFalse(model.canDeleteCurrentPaste)
        await model.deleteCurrentPaste()

        XCTAssertFalse(model.pending)
        XCTAssertNil(model.errorMessage)
    }

    func testSelectPasteEnablesDelete() {
        let model = AppModel(keychain: isolatedKeychain())
        let paste = CachedPaste(id: "p1", revisionID: "r1", sequence: 1, text: "hello", deleted: false, expiresAt: Date().addingTimeInterval(3600))

        model.selectPaste(paste)

        XCTAssertTrue(model.canDeleteCurrentPaste)
        XCTAssertEqual(model.draft, "hello")
    }

    func testSelectingDeletedPasteDoesNotEnableDelete() {
        let model = AppModel(keychain: isolatedKeychain())
        let paste = CachedPaste(id: "p1", revisionID: "r1", sequence: 1, text: nil, deleted: true, expiresAt: Date().addingTimeInterval(3600))

        model.selectPaste(paste)

        XCTAssertFalse(model.canDeleteCurrentPaste)
    }

    func testRetryNowClearsStaleErrorAndStampsSyncTime() async {
        let model = AppModel(keychain: isolatedKeychain())
        model.previewSeed(errorMessage: "Sync unavailable.")

        await model.retryNow()

        XCTAssertNil(model.errorMessage)
        XCTAssertNotNil(model.lastSyncedAt)
    }

    func testAttachmentFailureMessagesNameTheActualCause() {
        XCTAssertEqual(
            AppModel.attachmentFailureMessage(for: APIError.http(status: 400)),
            "The server rejected the images (HTTP 400)."
        )
        XCTAssertEqual(
            AppModel.attachmentFailureMessage(for: APIError.unauthorized),
            "This device is no longer signed in to the workspace."
        )
        XCTAssertEqual(
            AppModel.attachmentFailureMessage(for: APIError.notFound),
            "The server did not accept the images (HTTP 404) — the paste is gone, or this server has no attachment endpoint."
        )
        XCTAssertEqual(
            AppModel.attachmentFailureMessage(for: ImageNormalizationError.animated),
            "Animated images are not supported."
        )
        XCTAssertEqual(
            AppModel.attachmentFailureMessage(for: ImageNormalizationError.tooLarge),
            "A paste holds up to 8 images and 32 MB in total."
        )
    }

    func testActionErrorDoesNotReportConnectionAsFailed() {
        let model = AppModel(keychain: isolatedKeychain())

        model.previewSeed(errorMessage: "The server rejected the images (HTTP 400).")

        XCTAssertFalse(model.syncFailed)
        XCTAssertEqual(model.errorMessage, "The server rejected the images (HTTP 400).")
    }

    func testRetryNowClearsBothErrorKinds() async {
        let model = AppModel(keychain: isolatedKeychain())
        model.previewSeed(errorMessage: "Images could not be uploaded.", syncFailed: true)

        await model.retryNow()

        XCTAssertNil(model.errorMessage)
        XCTAssertFalse(model.syncFailed)
    }

    func testStartNewPasteClearsSelectionAndDraft() {
        let model = AppModel(keychain: isolatedKeychain())
        model.selectPaste(CachedPaste(id: "p1", revisionID: "r1", sequence: 1, text: "hello", deleted: false, expiresAt: Date().addingTimeInterval(3600)))

        model.startNewPaste()

        XCTAssertFalse(model.canDeleteCurrentPaste)
        XCTAssertEqual(model.draft, "")
        XCTAssertTrue(model.attachments.isEmpty)
    }

    func testSelectingImageOnlyPasteOpensItWithEmptyDraft() {
        let model = AppModel(keychain: isolatedKeychain())
        let paste = CachedPaste(
            id: "p1",
            revisionID: "r1",
            sequence: 1,
            text: nil,
            deleted: false,
            expiresAt: Date().addingTimeInterval(3600),
            attachmentRevisionID: "rev",
            attachments: [PasteAttachment(assetIndex: 0, mimeType: "image/png", width: 8, height: 8, byteSize: 4, expiresAt: Date().addingTimeInterval(3600))]
        )

        model.selectPaste(paste)

        XCTAssertTrue(model.canDeleteCurrentPaste)
        XCTAssertEqual(model.draft, "")
    }

    func testMergedAttachmentsAppendsWithinLimit() throws {
        let existing = (0..<3).map { NormalizedImage(mimeType: "image/png", width: 1, height: 1, data: Data([UInt8($0)])) }
        let incoming = (3..<5).map { NormalizedImage(mimeType: "image/png", width: 1, height: 1, data: Data([UInt8($0)])) }

        let merged = try AppModel.mergedAttachments(existing: existing, incoming: incoming)

        XCTAssertEqual(merged.map(\.data), (0..<5).map { Data([UInt8($0)]) })
    }

    func testMergedAttachmentsRejectsOverflowBeyondEight() {
        let existing = (0..<7).map { NormalizedImage(mimeType: "image/png", width: 1, height: 1, data: Data([UInt8($0)])) }
        let incoming = (7..<9).map { NormalizedImage(mimeType: "image/png", width: 1, height: 1, data: Data([UInt8($0)])) }

        XCTAssertThrowsError(try AppModel.mergedAttachments(existing: existing, incoming: incoming))
    }

    private func isolatedKeychain() -> KeychainStore {
        KeychainStore(service: "com.mcpaste.tests.\(UUID().uuidString)")
    }

    func testCopyRecoveryCodeWritesCodeToPasteboard() {
        let model = AppModel(keychain: KeychainStore())
        model.recoveryCode = "copy-test-code"
        NSPasteboard.general.clearContents()

        model.copyRecoveryCode()

        XCTAssertEqual(NSPasteboard.general.string(forType: .string), "copy-test-code")
        NSPasteboard.general.clearContents()
    }
}
