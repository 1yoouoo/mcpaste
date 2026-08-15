import Foundation
import XCTest
@testable import MCPasteCore

@MainActor
final class WorkspaceSessionTests: XCTestCase {
    func testSessionStartupCleansUnreferencedAttachmentFiles() throws {
        let cache = try SQLiteCache(path: ":memory:")
        let root = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = try PendingAttachmentStore(root: root)
        let manifest = try store.persist([
            NormalizedImage(mimeType: "image/png", width: 1, height: 1, data: Data([1]))
        ])
        XCTAssertTrue(FileManager.default.fileExists(atPath: manifest.items[0].path))

        _ = WorkspaceSession(
            cache: cache,
            coordinator: SyncCoordinator(api: ReplayWorkspaceAPI(), cache: cache),
            attachmentStore: store
        )

        XCTAssertFalse(FileManager.default.fileExists(atPath: manifest.items[0].path))
    }

    func testEnqueueAttachmentsPersistsManifestThroughSession() throws {
        let cache = try SQLiteCache(path: ":memory:")
        let coordinator = SyncCoordinator(api: ReplayWorkspaceAPI(), cache: cache)
        let root = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = try PendingAttachmentStore(root: root)
        let session = WorkspaceSession(cache: cache, coordinator: coordinator, attachmentStore: store)

        try session.enqueueAttachments(
            [NormalizedImage(mimeType: "image/png", width: 1, height: 1, data: Data([1, 2]))],
            pasteID: "paste-1"
        )

        let pending = try OfflineMutationQueue(cache: cache).pending()
        let manifest = try JSONDecoder().decode(PendingAttachmentManifest.self, from: try XCTUnwrap(pending.first?.body))
        XCTAssertEqual(pending.first?.kind, .replaceAttachments)
        XCTAssertEqual(manifest.items.count, 1)
        XCTAssertTrue(FileManager.default.fileExists(atPath: manifest.items[0].path))
        XCTAssertEqual(session.pendingCount, 1)
    }

    func testReplayUsesFIFORemappedIDAndCleansAttachmentFiles() throws {
        let cache = try SQLiteCache(path: ":memory:")
        let api = ReplayWorkspaceAPI()
        let queue = OfflineMutationQueue(cache: cache)
        let storeRoot = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: storeRoot) }
        let attachmentStore = try PendingAttachmentStore(root: storeRoot)
        let attachmentCacheRoot = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: attachmentCacheRoot) }
        let attachmentCache = try AttachmentCache(root: attachmentCacheRoot)
        let manifest = try attachmentStore.persist([
            NormalizedImage(mimeType: "image/png", width: 1, height: 1, data: Data([1, 2, 3]))
        ])
        let localID = "local:\(UUID().uuidString.lowercased())"
        _ = try queue.enqueue(kind: .create, pasteID: localID, body: JSONEncoder().encode(CreatePasteRequest(text: "created")))
        _ = try queue.enqueueAttachments(pasteID: localID, manifest: manifest)
        _ = try queue.enqueue(kind: .update, pasteID: localID, body: JSONEncoder().encode(CreatePasteRequest(text: "updated")))
        let expectedIdempotencyKeys = try queue.pending().map(\.idempotencyKey)

        let coordinator = SyncCoordinator(api: api, cache: cache)
        let session = WorkspaceSession(
            cache: cache,
            coordinator: coordinator,
            api: api,
            attachmentStore: attachmentStore,
            attachmentCache: attachmentCache
        )

        let replay = expectation(description: "replay")
        Task {
            await session.replayPending()
            replay.fulfill()
        }
        wait(for: [replay], timeout: 2)

        XCTAssertEqual(api.calls.map(\.kind), [.create, .replaceAttachments, .downloadAttachment, .update])
        XCTAssertEqual(api.calls[1].pasteID, api.serverPasteID)
        XCTAssertEqual(api.calls[2].pasteID, api.serverPasteID)
        XCTAssertEqual(api.calls.compactMap { $0.idempotencyKey }, expectedIdempotencyKeys)
        XCTAssertTrue(try queue.pending().isEmpty)
        XCTAssertFalse(FileManager.default.fileExists(atPath: manifest.items[0].path))
        let key = try AttachmentCache.Key(pasteID: api.serverPasteID, revisionID: api.serverAttachmentRevisionID, assetIndex: 0)
        XCTAssertEqual(try attachmentCache.data(for: key), Data([9, 8, 7]))
        XCTAssertEqual(session.resolvedPasteID(localID), api.serverPasteID)
    }

    func testReplayEmptyAttachmentReplacementRemovesStaleCache() throws {
        let cache = try SQLiteCache(path: ":memory:")
        let api = ReplayWorkspaceAPI(emptyAttachmentReplacement: true)
        let queue = OfflineMutationQueue(cache: cache)
        let storeRoot = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: storeRoot) }
        let cacheRoot = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: cacheRoot) }
        let attachmentStore = try PendingAttachmentStore(root: storeRoot)
        let attachmentCache = try AttachmentCache(root: cacheRoot)
        let staleRevisionID = "66666666-6666-4666-8666-666666666666"
        let staleKey = try AttachmentCache.Key(pasteID: api.serverPasteID, revisionID: staleRevisionID, assetIndex: 0)
        try attachmentCache.store(Data([5]), for: staleKey)
        let manifest = try attachmentStore.persist([])
        _ = try queue.enqueueAttachments(pasteID: api.serverPasteID, manifest: manifest)

        let coordinator = SyncCoordinator(api: api, cache: cache)
        let session = WorkspaceSession(cache: cache, coordinator: coordinator, api: api, attachmentStore: attachmentStore, attachmentCache: attachmentCache)
        let replay = expectation(description: "replay")
        Task {
            await session.replayPending()
            replay.fulfill()
        }
        wait(for: [replay], timeout: 2)

        XCTAssertNil(try attachmentCache.data(for: staleKey))
        XCTAssertTrue(try queue.pending().isEmpty)
    }

    func testConcurrentReplayRunsOnlyOneFIFOPass() async throws {
        let cache = try SQLiteCache(path: ":memory:")
        let api = ReplayWorkspaceAPI(createDelayNanoseconds: 100_000_000)
        let queue = OfflineMutationQueue(cache: cache)
        let localID = "local:\(UUID().uuidString.lowercased())"
        _ = try queue.enqueue(kind: .create, pasteID: localID, body: JSONEncoder().encode(CreatePasteRequest(text: "created")))

        let coordinator = SyncCoordinator(api: api, cache: cache)
        let session = WorkspaceSession(cache: cache, coordinator: coordinator, api: api)
        let first = Task { await session.replayPending() }
        try await Task.sleep(nanoseconds: 10_000_000)
        let second = Task { await session.replayPending() }
        await first.value
        await second.value

        XCTAssertEqual(api.calls.filter { $0.kind == .create }.count, 1)
        XCTAssertTrue(try queue.pending().isEmpty)
    }

    func testPartialAttachmentDownloadRetainsQueueAndRemovesPartialCache() throws {
        let cache = try SQLiteCache(path: ":memory:")
        let api = ReplayWorkspaceAPI(attachmentCount: 2, downloadErrorAfterFirst: true)
        let queue = OfflineMutationQueue(cache: cache)
        let storeRoot = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: storeRoot) }
        let cacheRoot = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: cacheRoot) }
        let attachmentStore = try PendingAttachmentStore(root: storeRoot)
        let attachmentCache = try AttachmentCache(root: cacheRoot)
        let manifest = try attachmentStore.persist([
            NormalizedImage(mimeType: "image/png", width: 1, height: 1, data: Data([1])),
            NormalizedImage(mimeType: "image/png", width: 1, height: 1, data: Data([2]))
        ])
        _ = try queue.enqueueAttachments(pasteID: api.serverPasteID, manifest: manifest)

        let coordinator = SyncCoordinator(api: api, cache: cache)
        let session = WorkspaceSession(cache: cache, coordinator: coordinator, api: api, attachmentStore: attachmentStore, attachmentCache: attachmentCache)
        let replay = expectation(description: "replay")
        Task {
            await session.replayPending()
            replay.fulfill()
        }
        wait(for: [replay], timeout: 2)

        XCTAssertEqual(try queue.pending().first?.attempts, 1)
        XCTAssertTrue(manifest.items.allSatisfy { FileManager.default.fileExists(atPath: $0.path) })
        for index in 0..<2 {
            let key = try AttachmentCache.Key(pasteID: api.serverPasteID, revisionID: api.serverAttachmentRevisionID, assetIndex: index)
            XCTAssertNil(try attachmentCache.data(for: key))
        }
    }

    func testAttachmentReplayWithoutStoreRetainsMutationWithoutRetrying() throws {
        let cache = try SQLiteCache(path: ":memory:")
        let api = ReplayWorkspaceAPI()
        let queue = OfflineMutationQueue(cache: cache)
        let manifest = PendingAttachmentManifest(items: [])
        _ = try queue.enqueueAttachments(pasteID: api.serverPasteID, manifest: manifest)

        let coordinator = SyncCoordinator(api: api, cache: cache)
        let session = WorkspaceSession(cache: cache, coordinator: coordinator, api: api)
        let replay = expectation(description: "replay")
        Task {
            await session.replayPending()
            replay.fulfill()
        }
        wait(for: [replay], timeout: 2)

        XCTAssertEqual(try queue.pending().count, 1)
        XCTAssertEqual(try queue.pending().first?.attempts, 1)
        XCTAssertTrue(api.calls.isEmpty)
        XCTAssertEqual(session.syncState, .failed)
    }

    func testReplayFailureIncrementsAttemptsRetainsFilesAndStopsFIFO() throws {
        let cache = try SQLiteCache(path: ":memory:")
        let api = ReplayWorkspaceAPI(createError: .transport)
        let queue = OfflineMutationQueue(cache: cache)
        let storeRoot = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: storeRoot) }
        let attachmentStore = try PendingAttachmentStore(root: storeRoot)
        let manifest = try attachmentStore.persist([
            NormalizedImage(mimeType: "image/png", width: 1, height: 1, data: Data([4]))
        ])
        let localID = "local:\(UUID().uuidString.lowercased())"
        let create = try queue.enqueue(kind: .create, pasteID: localID, body: JSONEncoder().encode(CreatePasteRequest(text: "created")))
        _ = try queue.enqueueAttachments(pasteID: localID, manifest: manifest)

        let coordinator = SyncCoordinator(api: api, cache: cache)
        let session = WorkspaceSession(cache: cache, coordinator: coordinator, api: api, attachmentStore: attachmentStore)
        let replay = expectation(description: "replay")
        Task {
            await session.replayPending()
            replay.fulfill()
        }
        wait(for: [replay], timeout: 2)

        let pending = try queue.pending()
        XCTAssertEqual(pending.map(\.id), [create.id, create.id + 1])
        XCTAssertEqual(pending.first?.attempts, 1)
        XCTAssertTrue(FileManager.default.fileExists(atPath: manifest.items[0].path))
        XCTAssertEqual(api.calls.map(\.kind), [.create])
        XCTAssertEqual(session.syncState, .failed)
    }

    func testCachedAttachmentsReturnsStoredImagesInAssetOrder() throws {
        let cache = try SQLiteCache(path: ":memory:")
        let cacheRoot = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: cacheRoot) }
        let attachmentCache = try AttachmentCache(root: cacheRoot)
        let pasteID = "11111111-1111-4111-8111-111111111111"
        let revisionID = "22222222-2222-4222-8222-222222222222"
        try attachmentCache.store(Data([1, 1]), for: try AttachmentCache.Key(pasteID: pasteID, revisionID: revisionID, assetIndex: 0))
        try attachmentCache.store(Data([2, 2]), for: try AttachmentCache.Key(pasteID: pasteID, revisionID: revisionID, assetIndex: 1))
        let session = WorkspaceSession(
            cache: cache,
            coordinator: SyncCoordinator(api: ReplayWorkspaceAPI(), cache: cache),
            attachmentCache: attachmentCache
        )
        let paste = CachedPaste(
            id: pasteID,
            revisionID: revisionID,
            sequence: 1,
            text: nil,
            deleted: false,
            expiresAt: Date().addingTimeInterval(3600),
            attachmentRevisionID: revisionID,
            attachments: [
                PasteAttachment(assetIndex: 0, mimeType: "image/png", width: 10, height: 10, byteSize: 2, expiresAt: Date().addingTimeInterval(3600)),
                PasteAttachment(assetIndex: 1, mimeType: "image/jpeg", width: 20, height: 20, byteSize: 2, expiresAt: Date().addingTimeInterval(3600))
            ]
        )

        let images = session.cachedAttachments(for: paste)

        XCTAssertEqual(images.map(\.data), [Data([1, 1]), Data([2, 2])])
        XCTAssertEqual(images.map(\.mimeType), ["image/png", "image/jpeg"])
        XCTAssertEqual(images.map(\.width), [10, 20])
    }

    func testCachedAttachmentsIsEmptyWithoutRevision() throws {
        let cache = try SQLiteCache(path: ":memory:")
        let session = WorkspaceSession(cache: cache, coordinator: SyncCoordinator(api: ReplayWorkspaceAPI(), cache: cache))
        let paste = CachedPaste(id: "p", revisionID: "r", sequence: 1, text: "hi", deleted: false, expiresAt: Date().addingTimeInterval(60))

        XCTAssertTrue(session.cachedAttachments(for: paste).isEmpty)
    }

    private func temporaryDirectory() throws -> URL {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("mcpaste-session-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        return root
    }

}

private final class ReplayWorkspaceAPI: WorkspaceAPI {
    struct Call {
        let kind: Kind
        let pasteID: String?
        let idempotencyKey: String?
    }

    enum Kind: Equatable { case create, replaceAttachments, update, downloadAttachment }

    let serverPasteID = "33333333-3333-4333-8333-333333333333"
    let serverAttachmentRevisionID = "44444444-4444-4444-8444-444444444444"
    let createError: APIError?
    let emptyAttachmentReplacement: Bool
    let createDelayNanoseconds: UInt64
    let attachmentCount: Int
    let downloadErrorAfterFirst: Bool
    private(set) var calls: [Call] = []

    init(
        createError: APIError? = nil,
        emptyAttachmentReplacement: Bool = false,
        createDelayNanoseconds: UInt64 = 0,
        attachmentCount: Int = 1,
        downloadErrorAfterFirst: Bool = false
    ) {
        self.createError = createError
        self.emptyAttachmentReplacement = emptyAttachmentReplacement
        self.createDelayNanoseconds = createDelayNanoseconds
        self.attachmentCount = attachmentCount
        self.downloadErrorAfterFirst = downloadErrorAfterFirst
    }

    func createPaste(text: String, idempotencyKey: String) async throws -> PasteRecord {
        calls.append(Call(kind: .create, pasteID: nil, idempotencyKey: idempotencyKey))
        if createDelayNanoseconds > 0 {
            try await Task.sleep(nanoseconds: createDelayNanoseconds)
        }
        if let createError { throw createError }
        return record(text: text, attachmentRevisionID: nil, attachments: [])
    }

    func updatePaste(id: String, text: String, idempotencyKey: String) async throws -> PasteRecord {
        calls.append(Call(kind: .update, pasteID: id, idempotencyKey: idempotencyKey))
        return record(text: text, attachmentRevisionID: serverAttachmentRevisionID, attachments: [])
    }

    func deletePaste(id: String, idempotencyKey: String) async throws {}

    func replaceAttachments(pasteID: String, images: [NormalizedImage], idempotencyKey: String) async throws -> PasteRecord {
        calls.append(Call(kind: .replaceAttachments, pasteID: pasteID, idempotencyKey: idempotencyKey))
        if emptyAttachmentReplacement {
            return record(text: "created", attachmentRevisionID: nil, attachments: [])
        }
        let attachments = (0..<attachmentCount).map { index in
            PasteAttachment(
                assetIndex: index,
                mimeType: "image/png",
                width: 1,
                height: 1,
                byteSize: 3,
                expiresAt: Date().addingTimeInterval(60)
            )
        }
        return record(text: "created", attachmentRevisionID: serverAttachmentRevisionID, attachments: attachments)
    }

    func downloadAttachment(pasteID: String, assetIndex: Int) async throws -> Data {
        calls.append(Call(kind: .downloadAttachment, pasteID: pasteID, idempotencyKey: nil))
        if downloadErrorAfterFirst && assetIndex > 0 { throw APIError.transport }
        return Data([9, 8, 7])
    }

    func sync(after: Int64) async throws -> SyncPage { SyncPage(cursor: after, hasMore: false, events: []) }
    func snapshot() async throws -> SnapshotPage { SnapshotPage(cursor: 0, pastes: []) }
    func eventStream(after: Int64) -> AsyncThrowingStream<Int64, Error> { AsyncThrowingStream { $0.finish() } }

    private func record(text: String, attachmentRevisionID: String?, attachments: [PasteAttachment]) -> PasteRecord {
        PasteRecord(
            id: serverPasteID,
            revisionID: "55555555-5555-4555-8555-555555555555",
            sequence: 1,
            text: text,
            deleted: false,
            expiresAt: Date().addingTimeInterval(60),
            attachmentRevisionID: attachmentRevisionID,
            attachments: attachments
        )
    }
}
