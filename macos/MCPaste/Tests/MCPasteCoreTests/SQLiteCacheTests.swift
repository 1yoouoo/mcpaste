import XCTest
import CSQLite
@testable import MCPasteCore

final class SQLiteCacheTests: XCTestCase {
    private func attachment(index: Int = 0, revision: String = "asset-r1") -> PasteAttachment {
        PasteAttachment(
            assetIndex: index,
            mimeType: "image/png",
            width: 40,
            height: 30,
            byteSize: 120,
            expiresAt: Date(timeIntervalSince1970: 1_800_000_000)
        )
    }

    private func temporaryDatabase() throws -> String {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("mcpaste-sqlite-\(UUID().uuidString)")
        return url.path
    }

    func testComponentEventsMergeAndSurviveRestart() throws {
        let path = try temporaryDatabase()
        defer { try? FileManager.default.removeItem(atPath: path) }
        let exact = "line 1\nline 2  "
        let cache = try SQLiteCache(path: path)

        try cache.apply(SyncEvent(
            sequence: 1,
            pasteID: "p1",
            revisionID: "text-r1",
            kind: .content,
            text: exact,
            deleted: false,
            attachments: nil
        ))
        try cache.apply(SyncEvent(
            sequence: 2,
            pasteID: "p1",
            revisionID: "asset-r1",
            kind: .attachmentBundle,
            text: nil,
            deleted: false,
            attachments: [attachment()]
        ))

        let restored = try SQLiteCache(path: path).paste(id: "p1")
        XCTAssertEqual(restored?.text, exact)
        XCTAssertEqual(restored?.attachments, [attachment()])
        XCTAssertEqual(restored?.attachmentRevisionID, "asset-r1")
        XCTAssertEqual(restored?.sequence, 2)
    }

    func testComponentMergePreservesTextAfterAttachmentClearAndLegacyImageBundle() throws {
        let cache = try SQLiteCache(path: ":memory:")
        try cache.apply(SyncEvent(sequence: 1, pasteID: "p1", revisionID: "text-r1", kind: .content, text: "text", deleted: false))
        try cache.apply(SyncEvent(sequence: 2, pasteID: "p1", revisionID: "asset-r1", kind: .imageBundle, text: nil, deleted: false, attachments: [attachment()]))
        XCTAssertEqual(try cache.paste(id: "p1")?.text, "text")
        XCTAssertEqual(try cache.paste(id: "p1")?.attachments.count, 1)

        try cache.apply(SyncEvent(sequence: 3, pasteID: "p1", revisionID: "asset-r2", kind: .attachmentBundle, text: nil, deleted: false, attachments: []))
        let cleared = try XCTUnwrap(try cache.paste(id: "p1"))
        XCTAssertEqual(cleared.text, "text")
        XCTAssertEqual(cleared.attachments, [])
        XCTAssertEqual(cleared.attachmentRevisionID, "asset-r2")
    }

    func testComponentSequencesRejectOnlyStaleSameComponent() throws {
        let cache = try SQLiteCache(path: ":memory:")
        try cache.apply(SyncEvent(sequence: 1, pasteID: "p1", revisionID: "text-r1", kind: .content, text: "first", deleted: false))
        try cache.apply(SyncEvent(sequence: 3, pasteID: "p1", revisionID: "asset-r1", kind: .attachmentBundle, text: nil, deleted: false, attachments: [attachment()]))

        try cache.apply(SyncEvent(sequence: 2, pasteID: "p1", revisionID: "text-r2", kind: .content, text: "second", deleted: false))
        try cache.apply(SyncEvent(sequence: 2, pasteID: "p1", revisionID: "asset-old", kind: .attachmentBundle, text: nil, deleted: false, attachments: [attachment(index: 1)]))

        let merged = try XCTUnwrap(try cache.paste(id: "p1"))
        XCTAssertEqual(merged.text, "second")
        XCTAssertEqual(merged.attachments.map(\.assetIndex), [0])
        XCTAssertEqual(merged.sequence, 3)
        XCTAssertEqual(merged.revisionID, "asset-r1")
    }

    func testTombstoneClearsAllComponentsAndStaleEventsCannotResurrectIt() throws {
        let cache = try SQLiteCache(path: ":memory:")
        try cache.apply(SyncEvent(sequence: 1, pasteID: "p1", revisionID: "text-r1", kind: .content, text: "text", deleted: false))
        try cache.apply(SyncEvent(sequence: 2, pasteID: "p1", revisionID: "asset-r1", kind: .attachmentBundle, text: nil, deleted: false, attachments: [attachment()]))
        try cache.apply(SyncEvent(sequence: 4, pasteID: "p1", revisionID: "delete-r1", kind: .tombstone, text: nil, deleted: true))
        try cache.apply(SyncEvent(sequence: 3, pasteID: "p1", revisionID: "text-old", kind: .content, text: "resurrect", deleted: false))

        let tombstone = try XCTUnwrap(try cache.paste(id: "p1"))
        XCTAssertTrue(tombstone.deleted)
        XCTAssertNil(tombstone.text)
        XCTAssertEqual(tombstone.attachments, [])
        XCTAssertNil(tombstone.attachmentRevisionID)
        XCTAssertEqual(tombstone.sequence, 4)
    }

    func testSnapshotReplacementIncludesAttachmentsAndRemovesStaleRows() throws {
        let cache = try SQLiteCache(path: ":memory:")
        try cache.apply(SyncEvent(sequence: 1, pasteID: "old", revisionID: "old-r1", kind: .content, text: "old", deleted: false))
        try cache.replacePastes([
            CachedPaste(
                id: "new",
                revisionID: "new-r1",
                sequence: 5,
                text: "new",
                deleted: false,
                expiresAt: Date(timeIntervalSince1970: 1_800_000_000),
                attachmentRevisionID: "asset-r1",
                attachments: [attachment()]
            )
        ])

        XCTAssertNil(try cache.paste(id: "old"))
        XCTAssertEqual(try cache.paste(id: "new")?.attachments, [attachment()])
    }

    func testExistingSQLiteSchemaMigrationPreservesText() throws {
        let path = try temporaryDatabase()
        defer { try? FileManager.default.removeItem(atPath: path) }

        var database: OpaquePointer?
        XCTAssertEqual(sqlite3_open_v2(path, &database, SQLITE_OPEN_CREATE | SQLITE_OPEN_READWRITE, nil), SQLITE_OK)
        let legacySQL = """
            CREATE TABLE paste_revisions (
                paste_id TEXT PRIMARY KEY,
                revision_id TEXT NOT NULL,
                sequence INTEGER NOT NULL,
                text TEXT,
                deleted INTEGER NOT NULL,
                expires_at REAL NOT NULL
            );
            INSERT INTO paste_revisions(paste_id, revision_id, sequence, text, deleted, expires_at)
            VALUES('p1', 'legacy-r1', 7, 'legacy exact text  ', 0, 1800000000);
            """
        XCTAssertEqual(sqlite3_exec(database, legacySQL, nil, nil, nil), SQLITE_OK)
        XCTAssertEqual(sqlite3_close(database), SQLITE_OK)
        database = nil

        let cache = try SQLiteCache(path: path)
        XCTAssertEqual(try cache.paste(id: "p1")?.text, "legacy exact text  ")
        XCTAssertEqual(try cache.paste(id: "p1")?.attachments, [])

        try cache.apply(SyncEvent(
            sequence: 8,
            pasteID: "p1",
            revisionID: "asset-r1",
            kind: .attachmentBundle,
            text: nil,
            deleted: false,
            attachments: [attachment()]
        ))
        XCTAssertEqual(try cache.paste(id: "p1")?.text, "legacy exact text  ")
        XCTAssertEqual(try cache.paste(id: "p1")?.attachments, [attachment()])
    }

    func testComponentSequenceMigrationDoesNotOverwriteIndependentProgressAfterRestart() throws {
        let path = try temporaryDatabase()
        defer { try? FileManager.default.removeItem(atPath: path) }

        do {
            let cache = try SQLiteCache(path: path)
            try cache.apply(SyncEvent(sequence: 1, pasteID: "p1", revisionID: "text-r1", kind: .content, text: "first", deleted: false))
            try cache.apply(SyncEvent(sequence: 3, pasteID: "p1", revisionID: "asset-r1", kind: .attachmentBundle, text: nil, deleted: false, attachments: [attachment()]))
        }

        let restarted = try SQLiteCache(path: path)
        try restarted.apply(SyncEvent(sequence: 2, pasteID: "p1", revisionID: "text-r2", kind: .content, text: "second", deleted: false))

        XCTAssertEqual(try restarted.paste(id: "p1")?.text, "second")
        XCTAssertEqual(try restarted.paste(id: "p1")?.attachments, [attachment()])
    }

    func testExactTextCursorAndTombstone() throws {
        let cache = try SQLiteCache(path: ":memory:")
        try cache.savePaste(CachedPaste(id: "p1", revisionID: "r1", sequence: 2, text: "line 1\r\n\nline 3", deleted: false, expiresAt: Date().addingTimeInterval(60)))
        XCTAssertEqual(try cache.paste(id: "p1")?.text, "line 1\r\n\nline 3")
        try cache.setCursor(2)
        XCTAssertEqual(try cache.cursor(), 2)
        try cache.savePaste(CachedPaste(id: "p1", revisionID: "r2", sequence: 3, text: nil, deleted: true, expiresAt: Date().addingTimeInterval(60)))
        XCTAssertNil(try cache.paste(id: "p1")?.text)
        XCTAssertTrue(try cache.paste(id: "p1")?.deleted == true)
    }
}
