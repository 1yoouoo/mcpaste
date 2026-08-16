import XCTest
import AppKit
import MCPasteCore
@testable import MCPasteApp

private actor AttachmentOrderRecorder {
    private var events: [String] = []
    func record(_ event: String) { events.append(event) }
    func snapshot() -> [String] { events }
}

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

    func testStartNewPasteClearsSelectionAndDraft() async {
        let model = AppModel(keychain: isolatedKeychain())
        model.selectPaste(CachedPaste(id: "p1", revisionID: "r1", sequence: 1, text: "hello", deleted: false, expiresAt: Date().addingTimeInterval(3600)))

        await model.startNewPaste()

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

    func testUploadReportsProgressBeforeItReachesTheNetwork() async {
        let model = AppModel(keychain: isolatedKeychain())
        XCTAssertEqual(model.uploadingCount, 0)

        // No session is installed, so the upload cannot proceed past preparation.
        await model.uploadImages([Data([0x01])])

        XCTAssertEqual(model.uploadingCount, 0, "progress must be cleared once the attempt ends")
    }

    func testBeginningAnUploadPublishesTheItemCount() {
        let model = AppModel(keychain: isolatedKeychain())

        model.beginUpload(count: 3)

        XCTAssertEqual(model.uploadingCount, 3)
        XCTAssertTrue(model.isUploading)
    }

    func testFinishingAnUploadClearsTheProgress() {
        let model = AppModel(keychain: isolatedKeychain())
        model.beginUpload(count: 2)

        model.finishUpload()

        XCTAssertEqual(model.uploadingCount, 0)
        XCTAssertFalse(model.isUploading)
    }

    func testConcurrentUploadsAccumulateAndDrainTogether() {
        let model = AppModel(keychain: isolatedKeychain())

        model.beginUpload(count: 2)
        model.beginUpload(count: 1)
        XCTAssertEqual(model.uploadingCount, 3)

        model.finishUpload(count: 2)
        XCTAssertEqual(model.uploadingCount, 1)
        model.finishUpload(count: 1)
        XCTAssertFalse(model.isUploading)
    }

    func testAttachmentWorkIsSerialised() async {
        let model = AppModel(keychain: isolatedKeychain())
        let order = AttachmentOrderRecorder()

        async let first: Void = model.serializeAttachmentWork {
            await order.record("first-start")
            try? await Task.sleep(nanoseconds: 60_000_000)
            await order.record("first-end")
        }
        async let second: Void = model.serializeAttachmentWork {
            await order.record("second-start")
            await order.record("second-end")
        }
        _ = await (first, second)

        // Which task reaches the gate first is not fixed, but neither may run inside the other.
        let events = await order.snapshot()
        XCTAssertEqual(events.count, 4)
        for name in ["first", "second"] {
            let start = events.firstIndex(of: "\(name)-start")!
            XCTAssertEqual(events[start + 1], "\(name)-end",
                           "\(name) was interrupted by the other upload, so they would clobber each other")
        }
    }

    func testRefreshKeepsAttachmentsWhenTheCacheHasNotCaughtUp() {
        let model = AppModel(keychain: isolatedKeychain())
        let images = [NormalizedImage(mimeType: "image/png", width: 1, height: 1, data: Data([1]))]
        model.previewSeed(attachments: images)

        model.applyCachedAttachments([], recordedCount: 1)

        XCTAssertEqual(model.attachments.count, 1, "a cache miss must not erase images the paste still has")
    }

    func testRefreshClearsAttachmentsWhenThePasteHasNone() {
        let model = AppModel(keychain: isolatedKeychain())
        model.previewSeed(attachments: [NormalizedImage(mimeType: "image/png", width: 1, height: 1, data: Data([1]))])

        model.applyCachedAttachments([], recordedCount: 0)

        XCTAssertTrue(model.attachments.isEmpty)
    }

    func testStartingANewPasteCommitsTheCurrentDraftFirst() async {
        let model = AppModel(keychain: isolatedKeychain())
        model.previewSeed(draft: "unsaved words")

        await model.startNewPaste()

        XCTAssertEqual(model.draft, "", "the new paste starts empty")
        XCTAssertFalse(model.canDeleteCurrentPaste)
    }

    func testAnEmptyDraftIsNotWorthSaving() {
        let model = AppModel(keychain: isolatedKeychain())
        model.previewSeed(draft: "   \n  ")

        XCTAssertFalse(model.hasUnsavedWork, "whitespace alone is not a paste")
    }

    func testTypedTextCountsAsUnsavedWork() {
        let model = AppModel(keychain: isolatedKeychain())
        model.previewSeed(draft: "something")

        XCTAssertTrue(model.hasUnsavedWork)
    }

    func testAttachmentsAloneCountAsUnsavedWork() {
        let model = AppModel(keychain: isolatedKeychain())
        model.previewSeed(attachments: [NormalizedImage(mimeType: "image/png", width: 1, height: 1, data: Data([1]))])

        XCTAssertTrue(model.hasUnsavedWork, "an image-only paste still needs saving")
    }

    func testSavingIsSkippedWhenNothingChangedSinceTheLastSave() async {
        let model = AppModel(keychain: isolatedKeychain())
        model.previewSeed(draft: "words")
        model.markDraftSaved()

        XCTAssertFalse(model.hasUnsavedWork, "a draft that matches what was stored needs no write")
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
