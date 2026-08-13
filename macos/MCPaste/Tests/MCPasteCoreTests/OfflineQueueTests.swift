import XCTest
@testable import MCPasteCore

final class OfflineQueueTests: XCTestCase {
    func testIdempotencyKeyIsStableAcrossRetriesAndOrderingIsFIFO() throws {
        let cache = try SQLiteCache(path: ":memory:")
        let queue = OfflineMutationQueue(cache: cache)
        let first = try queue.enqueue(kind: .create, pasteID: "p1", body: Data("a".utf8))
        _ = try queue.enqueue(kind: .update, pasteID: "p1", body: Data("b".utf8))
        XCTAssertEqual(try queue.pending().map(\.id), [first.id, 2])
        XCTAssertEqual(try queue.retry(first.id).idempotencyKey, first.idempotencyKey)
        XCTAssertEqual(try queue.pending().first?.attempts, 1)
    }
}
