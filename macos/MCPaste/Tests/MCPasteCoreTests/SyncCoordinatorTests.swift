import XCTest
@testable import MCPasteCore

final class SyncCoordinatorTests: XCTestCase {
    func testSyncAppliesServerOrderAndPersistsCursor() async throws {
        let cache = try SQLiteCache(path: ":memory:")
        let api = StubSyncAPI(events: [
            SyncEvent(sequence: 1, pasteID: "p1", revisionID: "r1", kind: .content, text: "hello", deleted: false),
            SyncEvent(sequence: 2, pasteID: "p1", revisionID: "r2", kind: .tombstone, text: nil, deleted: true)
        ])
        let coordinator = SyncCoordinator(api: api, cache: cache)
        try await coordinator.sync()
        XCTAssertEqual(try cache.cursor(), 2)
        XCTAssertTrue(try cache.paste(id: "p1")?.deleted == true)
    }
}

private final class StubSyncAPI: SyncAPI {
    let events: [SyncEvent]
    init(events: [SyncEvent]) { self.events = events }
    func sync(after: Int64) async throws -> SyncPage { SyncPage(cursor: events.last?.sequence ?? after, hasMore: false, events: events.filter { $0.sequence > after }) }
}
