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

    func testExpiredCursorResetsOnceAndReplaysFromZero() async throws {
        let cache = try SQLiteCache(path: ":memory:")
        let api = ExpiringSyncAPI()
        let coordinator = SyncCoordinator(api: api, cache: cache)
        try await coordinator.sync()
        XCTAssertEqual(api.requestedAfter, [0, 0])
        XCTAssertEqual(try cache.cursor(), 1)
    }

    func testExpiredCursorSnapshotRemovesStaleCachedPlaintext() async throws {
        let cache = try SQLiteCache(path: ":memory:")
        try cache.savePaste(CachedPaste(id: "stale", revisionID: "old", sequence: 1, text: "must disappear", deleted: false, expiresAt: Date()))
        let api = ExpiringSnapshotAPI()
        let coordinator = SyncCoordinator(api: api, cache: cache)
        try await coordinator.sync()
        XCTAssertNil(try cache.paste(id: "stale"))
        XCTAssertEqual(try cache.paste(id: "fresh")?.text, "fresh text")
        XCTAssertEqual(try cache.cursor(), 4)
    }
}

private final class StubSyncAPI: SyncAPI {
    let events: [SyncEvent]
    init(events: [SyncEvent]) { self.events = events }
    func sync(after: Int64) async throws -> SyncPage { SyncPage(cursor: events.last?.sequence ?? after, hasMore: false, events: events.filter { $0.sequence > after }) }
}

private final class ExpiringSyncAPI: SyncAPI {
    private(set) var requestedAfter: [Int64] = []
    func sync(after: Int64) async throws -> SyncPage {
        requestedAfter.append(after)
        if requestedAfter.count == 1 { throw APIError.cursorExpired }
        return SyncPage(cursor: 1, hasMore: false, events: [SyncEvent(sequence: 1, pasteID: "p", revisionID: "r", kind: .content, text: "ok", deleted: false)])
    }
}

private final class ExpiringSnapshotAPI: SyncAPI, SnapshotAPI {
    private var expired = true
    func sync(after: Int64) async throws -> SyncPage {
        if expired { expired = false; throw APIError.cursorExpired }
        return SyncPage(cursor: after, hasMore: false, events: [])
    }
    func snapshot() async throws -> SnapshotPage {
        SnapshotPage(cursor: 4, pastes: [PasteRecord(id: "fresh", revisionID: "new", sequence: 4, text: "fresh text", deleted: false, expiresAt: Date())])
    }
}
