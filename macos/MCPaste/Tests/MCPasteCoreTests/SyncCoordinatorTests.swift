import XCTest
@testable import MCPasteCore

final class SyncCoordinatorTests: XCTestCase {
    func testConcurrentSyncCallsAreSerialized() async throws {
        let cache = try SQLiteCache(path: ":memory:")
        let api = ConcurrentSyncAPI()
        let coordinator = SyncCoordinator(api: api, cache: cache)

        async let first: Void = coordinator.sync()
        async let second: Void = coordinator.sync()
        try await first
        try await second

        let maximum = await api.maxActive()
        XCTAssertEqual(maximum, 1)
    }

    func testSyncMergesContentAndAttachmentComponents() async throws {
        let cache = try SQLiteCache(path: ":memory:")
        let exact = "line 1\nline 2  "
        let attachment = PasteAttachment(
            assetIndex: 0,
            mimeType: "image/png",
            width: 40,
            height: 30,
            byteSize: 120,
            expiresAt: Date(timeIntervalSince1970: 1_800_000_000)
        )
        let api = StubSyncAPI(events: [
            SyncEvent(sequence: 1, pasteID: "p1", revisionID: "t1", kind: .content, text: exact, deleted: false),
            SyncEvent(sequence: 2, pasteID: "p1", revisionID: "a1", kind: .attachmentBundle, text: nil, deleted: false, attachments: [attachment])
        ])

        try await SyncCoordinator(api: api, cache: cache).sync()

        let paste = try XCTUnwrap(cache.paste(id: "p1"))
        XCTAssertEqual(paste.text, exact)
        XCTAssertEqual(paste.attachmentRevisionID, "a1")
        XCTAssertEqual(paste.attachments, [attachment])
    }

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

    func testExpiredCursorSnapshotThenReplaysLaterEvents() async throws {
        let cache = try SQLiteCache(path: ":memory:")
        let api = ExpiringSnapshotThenEventsAPI()
        let coordinator = SyncCoordinator(api: api, cache: cache)

        try await coordinator.sync()

        XCTAssertEqual(api.requestedAfter, [0, 4])
        XCTAssertEqual(try cache.paste(id: "fresh")?.text, "fresh text")
        XCTAssertEqual(try cache.paste(id: "later")?.text, "later text")
        XCTAssertEqual(try cache.cursor(), 5)
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

private final class ExpiringSnapshotThenEventsAPI: SyncAPI, SnapshotAPI {
    private var expired = true
    private(set) var requestedAfter: [Int64] = []

    func sync(after: Int64) async throws -> SyncPage {
        requestedAfter.append(after)
        if expired {
            expired = false
            throw APIError.cursorExpired
        }
        return SyncPage(
            cursor: 5,
            hasMore: false,
            events: [SyncEvent(sequence: 5, pasteID: "later", revisionID: "later-r", kind: .content, text: "later text", deleted: false)]
        )
    }

    func snapshot() async throws -> SnapshotPage {
        SnapshotPage(
            cursor: 4,
            pastes: [PasteRecord(id: "fresh", revisionID: "fresh-r", sequence: 4, text: "fresh text", deleted: false, expiresAt: Date())]
        )
    }
}

private final class ConcurrentSyncAPI: SyncAPI {
    private let activity = SyncActivity()

    func sync(after: Int64) async throws -> SyncPage {
        await activity.begin()
        try await Task.sleep(nanoseconds: 30_000_000)
        await activity.end()
        return SyncPage(cursor: after + 1, hasMore: false, events: [])
    }

    func maxActive() async -> Int { await activity.maximum }
}

private actor SyncActivity {
    private(set) var active = 0
    private(set) var maximum = 0

    func begin() {
        active += 1
        maximum = max(maximum, active)
    }

    func end() { active -= 1 }
}
