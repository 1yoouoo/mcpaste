import XCTest
@testable import MCPasteCore

final class OfflineQueueTests: XCTestCase {
    func testExplicitIdempotencyKeySurvivesQueueing() throws {
        let cache = try SQLiteCache(path: ":memory:")
        let queue = OfflineMutationQueue(cache: cache)

        let mutation = try queue.enqueue(
            kind: .create,
            pasteID: "p1",
            body: Data("a".utf8),
            idempotencyKey: "request-key"
        )

        XCTAssertEqual(mutation.idempotencyKey, "request-key")
        XCTAssertEqual(try queue.pending().first?.idempotencyKey, "request-key")
    }

    func testIdempotencyKeyIsStableAcrossRetriesAndOrderingIsFIFO() throws {
        let cache = try SQLiteCache(path: ":memory:")
        let queue = OfflineMutationQueue(cache: cache)
        let first = try queue.enqueue(kind: .create, pasteID: "p1", body: Data("a".utf8))
        _ = try queue.enqueue(kind: .update, pasteID: "p1", body: Data("b".utf8))
        XCTAssertEqual(try queue.pending().map(\.id), [first.id, 2])
        XCTAssertEqual(try queue.retry(first.id).idempotencyKey, first.idempotencyKey)
        XCTAssertEqual(try queue.pending().first?.attempts, 1)
    }

    func testAttachmentMutationAndLocalIDRemapPreserveFIFOAndAttachmentRows() throws {
        let cache = try SQLiteCache(path: ":memory:")
        let queue = OfflineMutationQueue(cache: cache)
        let localID = "local:\(UUID().uuidString.lowercased())"
        try cache.savePaste(CachedPaste(
            id: localID,
            revisionID: "local-r1",
            sequence: 1,
            text: "offline",
            deleted: false,
            expiresAt: Date(timeIntervalSince1970: 1_800_000_000),
            attachmentRevisionID: "asset-r1",
            attachments: [PasteAttachment(
                assetIndex: 0,
                mimeType: "image/png",
                width: 1,
                height: 1,
                byteSize: 1,
                expiresAt: Date(timeIntervalSince1970: 1_800_000_000)
            )]
        ))
        let manifest = PendingAttachmentManifest(items: [
            .init(path: "/tmp/pending-image", mimeType: "image/png", width: 1, height: 1)
        ])

        let create = try queue.enqueue(kind: .create, pasteID: localID, body: Data("create".utf8))
        let update = try queue.enqueue(kind: .update, pasteID: localID, body: Data("update".utf8))
        let attachments = try queue.enqueueAttachments(pasteID: localID, manifest: manifest)
        let delete = try queue.enqueue(kind: .delete, pasteID: localID, body: Data())
        XCTAssertEqual(try queue.pending().map(\.id), [create.id, update.id, attachments.id, delete.id])

        try cache.remapPasteID(from: localID, to: "server-p1")

        XCTAssertNil(try cache.paste(id: localID))
        XCTAssertEqual(try cache.paste(id: "server-p1")?.attachments.map(\.assetIndex), [0])
        XCTAssertEqual(try queue.pending().map(\.pasteID), ["server-p1", "server-p1", "server-p1", "server-p1"])
        XCTAssertEqual(try JSONDecoder().decode(PendingAttachmentManifest.self, from: attachments.body), manifest)

        try cache.removePending(forPasteID: "server-p1")
        XCTAssertTrue(try queue.pending().isEmpty)
    }
}
